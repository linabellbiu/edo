package k3s

func validateDatastoreSelection(input Input) error {
	if input.Datastore == "" {
		return nil
	}
	if input.Action != "create" || input.Mode != "native" {
		return &ValidationError{Message: "数据库类型仅可在新建原生 K3s 集群时选择"}
	}
	if input.Datastore != "sqlite" && input.Datastore != "etcd" {
		return &ValidationError{Message: "请选择 SQLite 或内嵌 etcd"}
	}
	return nil
}

func applyDatastore(input *Input) error {
	// 未指定类型的调用沿用原有自定义配置，保留外部数据库能力。
	if input.Datastore == "" {
		return nil
	}
	if endpoint, _ := input.ServerOptions["datastore-endpoint"].(string); endpoint != "" {
		return &ValidationError{Message: "已配置自定义数据库连接，请先清除该连接再选择 SQLite 或内嵌 etcd"}
	}
	etcd := input.Datastore == "etcd"
	if value, exists := input.ServerOptions["cluster-init"]; exists && value != etcd {
		return &ValidationError{Message: "高级配置的内嵌 etcd 初始化选项与数据库类型冲突，请调整后重新检测"}
	}
	if input.ServerOptions == nil {
		input.ServerOptions = map[string]any{}
	}
	if etcd {
		input.ServerOptions["cluster-init"] = true
	} else {
		delete(input.ServerOptions, "cluster-init")
	}
	return nil
}
