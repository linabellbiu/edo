package projecttransfer

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"edo/internal/database"
	"edo/internal/dockerengine"
	"edo/internal/model"
)

const maxSelection = 100

var (
	ErrInvalid      = errors.New("请选择有效的资源和同部门目标项目")
	ErrNotFound     = errors.New("待移动的资源或项目不存在")
	ErrConflict     = errors.New("资源仍有依赖、正在使用或与目标项目的资源冲突，请重新预览")
	ErrConfirmation = errors.New("移动范围已变化，请重新预览并确认")
	ErrForbidden    = errors.New("需要同时拥有源项目和目标项目的资源移动权限")
)

type Resource struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type Input struct {
	TargetProjectID string                     `json:"target_project_id"`
	Resources       []Resource                 `json:"resources"`
	Confirmation    string                     `json:"confirmation"`
	Authorize       func(*gorm.DB) error       `json:"-"`
	Audit           func(*gorm.DB, Plan) error `json:"-"`
}

type Item struct {
	Resource
	Name      string `json:"name"`
	Automatic bool   `json:"automatic"`
}

type Blocker struct {
	Resource
	Name    string `json:"name,omitempty"`
	Message string `json:"message"`
}

type Plan struct {
	SourceProjectID string    `json:"source_project_id"`
	TargetProjectID string    `json:"target_project_id"`
	Resources       []Item    `json:"resources"`
	Blockers        []Blocker `json:"blockers"`
	Confirmation    string    `json:"confirmation"`
	HistoryCount    int64     `json:"history_count"`
	records         []rootRow
	history         historyPlan
}

type Kind struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

type Catalog struct {
	Kinds     []Kind `json:"kinds"`
	Resources []Item `json:"resources"`
}

type Service struct {
	db     *gorm.DB
	logger *slog.Logger
}

func New(db *gorm.DB, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{db: db, logger: logger}
}

func (s *Service) Catalog(ctx context.Context, source string) (Catalog, error) {
	result := Catalog{Kinds: []Kind{}, Resources: []Item{}}
	if _, err := uuid.Parse(source); err != nil {
		return result, ErrInvalid
	}
	for _, kind := range kinds() {
		d := definitions[kind]
		result.Kinds = append(result.Kinds, Kind{Kind: kind, Label: d.label})
		var rows []rootRow
		if err := movableRoots(rootQuery(s.db.WithContext(ctx), d, source, false), kind).Select("uuid AS id, " + d.nameSQL + " AS name").Order("uuid").Find(&rows).Error; err != nil {
			s.logger.ErrorContext(ctx, "读取移动资源目录失败", "operation", "project_transfer_catalog", "project_id", source, "kind", kind, "err", err)
			return Catalog{}, err
		}
		for _, row := range rows {
			result.Resources = append(result.Resources, Item{Resource: Resource{Kind: kind, ID: row.ID}, Name: row.Name})
		}
	}
	return result, nil
}

func (s *Service) Preview(ctx context.Context, source string, input Input) (Plan, error) {
	return s.run(ctx, source, input, false)
}
func (s *Service) Move(ctx context.Context, source string, input Input) (Plan, error) {
	return s.run(ctx, source, input, true)
}

func (s *Service) run(ctx context.Context, source string, input Input, commit bool) (Plan, error) {
	plan := Plan{SourceProjectID: source, TargetProjectID: input.TargetProjectID, Resources: []Item{}, Blockers: []Blocker{}}
	actor, _ := database.DepartmentScopeFromContext(ctx)
	err := validateInput(source, input)
	if err == nil && (input.Authorize == nil || commit && input.Audit == nil) {
		err = ErrForbidden
	}
	if err == nil {
		// 所有未限定模型的访问都必须由固定类型目录和显式项目范围约束。
		neutral := database.WithDepartmentScope(ctx, database.DepartmentScope{UserID: actor.UserID, AllDepartments: true})
		err = s.db.WithContext(neutral).Transaction(func(tx *gorm.DB) error {
			if err := checkProjects(tx, source, input.TargetProjectID, commit); err != nil {
				return err
			}
			if input.Authorize != nil {
				if err := input.Authorize(tx); err != nil {
					return err
				}
			}
			if err := buildPlan(tx, &plan, input.Resources, commit); err != nil {
				return err
			}
			if len(plan.Blockers) == 0 {
				plan.Confirmation = s.confirm(actor.UserID, plan)
			}
			if !commit {
				return nil
			}
			if len(plan.Blockers) > 0 {
				return ErrConflict
			}
			if !hmac.Equal([]byte(plan.Confirmation), []byte(input.Confirmation)) {
				return ErrConfirmation
			}
			if err := moveRows(tx, plan); err != nil {
				return err
			}
			if input.Audit != nil {
				return input.Audit(tx, plan)
			}
			return nil
		})
	}
	if err != nil {
		s.logger.WarnContext(ctx, "项目资源移动未完成", "operation", "project_resource_transfer", "source_project_id", source, "target_project_id", input.TargetProjectID, "actor_user_id", actor.UserID, "commit", commit, "err", err)
	}
	return plan, err
}

func validateInput(source string, input Input) error {
	if len(input.Resources) < 1 || len(input.Resources) > maxSelection || source == input.TargetProjectID {
		return ErrInvalid
	}
	for _, id := range []string{source, input.TargetProjectID} {
		v, err := uuid.Parse(id)
		if err != nil || v.String() != id {
			return ErrInvalid
		}
	}
	for _, item := range input.Resources {
		v, err := uuid.Parse(item.ID)
		if _, ok := definitions[item.Kind]; !ok || err != nil || v.String() != item.ID {
			return ErrInvalid
		}
	}
	return nil
}

func checkProjects(tx *gorm.DB, source, target string, lock bool) error {
	ids := []string{source, target}
	slices.Sort(ids)
	var projects []model.Project
	if lock {
		// 先写后读取得 SQLite 写锁，也防止两个项目之间相向移动的锁顺序不同。
		for _, id := range ids {
			if err := tx.Model(&model.Project{}).Where("uuid = ?", id).UpdateColumn("updated_at", gorm.Expr("updated_at")).Error; err != nil {
				return err
			}
		}
	}
	q := tx.Where("uuid IN ? AND is_active = ?", ids, true).Order("uuid")
	if lock {
		q = q.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := q.Find(&projects).Error; err != nil {
		return err
	}
	if len(projects) != 2 {
		return ErrNotFound
	}
	if projects[0].DepartmentID != projects[1].DepartmentID {
		return ErrInvalid
	}
	return nil
}

type rootRow struct {
	Kind      string `gorm:"-"`
	ID        string
	Name      string
	ParentID  string
	UpdatedAt string
	Archived  bool
}

func kinds() []string {
	keys := make([]string, 0, len(definitions))
	for kind := range definitions {
		keys = append(keys, kind)
	}
	slices.Sort(keys)
	return keys
}

func rootQuery(tx *gorm.DB, d definition, project string, includeArchived bool) *gorm.DB {
	q := tx.Table(d.table).Where("project_id = ?", project)
	if d.softDelete && !includeArchived {
		q = q.Where("deleted_at IS NULL")
	}
	return q
}

func movableRoots(q *gorm.DB, kind string) *gorm.DB {
	switch kind {
	case "host":
		return q.Where("is_builtin = ?", false)
	case "docker_endpoint":
		return q.Where("uuid <> ?", dockerengine.LocalEndpointID)
	}
	return q
}

func rows(tx *gorm.DB, kind, project string, includeArchived bool, lockIDs []string) ([]rootRow, error) {
	d := definitions[kind]
	fields := "uuid AS id, " + d.nameSQL + " AS name, updated_at"
	if d.parentField != "" {
		fields += ", " + d.parentField + " AS parent_id"
	}
	if d.softDelete {
		fields += ", CASE WHEN deleted_at IS NULL THEN false ELSE true END AS archived"
	}
	q := movableRoots(rootQuery(tx, d, project, includeArchived), kind).Select(fields).Order("uuid")
	if len(lockIDs) > 0 {
		q = q.Where("uuid IN ?", lockIDs).Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var found []rootRow
	if err := q.Find(&found).Error; err != nil {
		return nil, fmt.Errorf("读取项目资源 %s: %w", kind, err)
	}
	for i := range found {
		found[i].Kind = kind
	}
	return found, nil
}

func buildPlan(tx *gorm.DB, plan *Plan, requested []Resource, lock bool) error {
	selected := map[resourceKey]bool{}
	source := map[resourceKey]rootRow{}
	target := map[resourceKey]rootRow{}
	for _, kind := range kinds() {
		// 保留已归档的流水线及其历史归属。
		found, err := rows(tx, kind, plan.SourceProjectID, definitions[kind].parentKind != "", nil)
		if err != nil {
			return err
		}
		for _, item := range found {
			source[resourceKey{kind, item.ID}] = item
		}
		found, err = rows(tx, kind, plan.TargetProjectID, false, nil)
		if err != nil {
			return err
		}
		for _, item := range found {
			target[resourceKey{kind, item.ID}] = item
		}
	}
	for _, item := range requested {
		key := resourceKey{item.Kind, item.ID}
		row, ok := source[key]
		if !ok || row.Archived {
			return ErrNotFound
		}
		selected[key] = false
	}
	for key, row := range source {
		if d := definitions[key.kind]; d.parentKind != "" {
			if _, ok := selected[resourceKey{d.parentKind, row.ParentID}]; ok {
				if _, explicit := selected[key]; !explicit {
					selected[key] = true
				}
			}
		}
	}
	if len(selected) > 1000 {
		return ErrInvalid
	}
	add := func(key resourceKey, message string) {
		r := source[key]
		name := r.Name
		if name == "" {
			name = externalDependencyLabels[key.kind]
			if name == "" {
				name = definitions[key.kind].label
			}
		}
		plan.Blockers = append(plan.Blockers, Blocker{Resource: Resource{Kind: key.kind, ID: key.id}, Name: name, Message: message})
	}
	keys := make([]resourceKey, 0, len(selected))
	for key := range selected {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, compareKeys)
	for _, key := range keys {
		r := source[key]
		if !r.Archived {
			plan.Resources = append(plan.Resources, Item{Resource: Resource{Kind: key.kind, ID: key.id}, Name: r.Name, Automatic: selected[key]})
		}
		plan.records = append(plan.records, r)
	}
	if lock {
		// 锁定实际移动的行，避免一个迁移等待阻塞同项目的无关资源编辑。
		groups := rootIDs(plan.records)
		for _, kind := range kinds() {
			ids := groups[kind]
			if len(ids) == 0 {
				continue
			}
			current, err := rows(tx, kind, plan.SourceProjectID, definitions[kind].parentKind != "", ids)
			if err != nil {
				return err
			}
			if len(current) != len(ids) {
				return ErrConfirmation
			}
			for _, row := range current {
				if row != source[resourceKey{kind, row.ID}] {
					return ErrConfirmation
				}
			}
		}
	}
	if err := checkConflicts(tx, plan, add); err != nil {
		return err
	}
	graph, err := discoverDependencies(tx, []string{plan.SourceProjectID, plan.TargetProjectID})
	if err != nil {
		return err
	}
	for user, refs := range graph {
		_, moves := selected[user]
		for _, ref := range refs {
			_, refMoves := selected[ref]
			if moves && !refMoves {
				if _, ok := target[ref]; !ok {
					if def := definitions[user.kind]; def.parentKind == ref.kind && source[user].ParentID == ref.id {
						add(ref, "应用专属资源必须与所属应用一起移动")
					} else {
						add(ref, "移动的资源依赖此资源，请一并选择或先解除引用")
					}
				}
			}
			if refMoves && !moves {
				if _, ok := target[user]; !ok {
					add(user, "此资源仍引用待移动的资源，请一并选择或先解除引用")
				}
			}
		}
	}
	if err := buildHistory(tx, plan, selected, add); err != nil {
		return err
	}
	slices.SortFunc(plan.Blockers, func(a, b Blocker) int {
		if v := compareKeys(resourceKey{a.Kind, a.ID}, resourceKey{b.Kind, b.ID}); v != 0 {
			return v
		}
		return strings.Compare(a.Message, b.Message)
	})
	plan.Blockers = slices.Compact(plan.Blockers)
	return nil
}

func rootIDs(records []rootRow) map[string][]string {
	ids := make(map[string][]string)
	for _, row := range records {
		ids[row.Kind] = append(ids[row.Kind], row.ID)
	}
	return ids
}

func checkConflicts(tx *gorm.DB, plan *Plan, add func(resourceKey, string)) error {
	ids := rootIDs(plan.records)
	for _, kind := range kinds() {
		d := definitions[kind]
		if len(ids[kind]) == 0 || len(d.uniqueFields) == 0 {
			continue
		}
		// 按类型批量读取，使用目标数据库的名称比较规则（含占用名称的归档记录）。
		fields := make([]string, 0, len(d.uniqueFields))
		for _, f := range d.uniqueFields {
			fields = append(fields, "candidate."+f+" = original."+f)
		}
		var conflicts []string
		if err := tx.Table(d.table+" AS candidate").Joins("JOIN "+d.table+" AS original ON "+strings.Join(fields, " AND ")).
			Where("candidate.project_id = ? AND original.uuid IN ?", plan.TargetProjectID, ids[kind]).
			Distinct("original.uuid").Pluck("original.uuid", &conflicts).Error; err != nil {
			return err
		}
		for _, id := range conflicts {
			add(resourceKey{kind, id}, "目标项目已有相同名称或标识的资源")
		}
	}
	return nil
}

func compareKeys(a, b resourceKey) int {
	if v := strings.Compare(a.kind, b.kind); v != 0 {
		return v
	}
	return strings.Compare(a.id, b.id)
}

func (s *Service) confirm(actor string, p Plan) string {
	// 指纹仅检测预览变化，不能替代实际操作时的 RBAC 或关系复核；多个 API 实例可独立校验。
	encoded, _ := json.Marshal([]any{actor, p.SourceProjectID, p.TargetProjectID, p.Resources, p.records, p.history.Fingerprint()})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
