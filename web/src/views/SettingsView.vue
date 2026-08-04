<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from "vue";
import { useRoute, useRouter } from "vue-router";
import { Modal, message } from "ant-design-vue";
import { Database, FolderCog, KeyRound, RefreshCw, ScrollText, ShieldCheck, Trash2 } from "lucide-vue-next";

import client from "@/api/client";
import { apiErrorMessage } from "@/api/resources";
import PageToolbar from "@/components/PageToolbar.vue";
import IdentityProvidersView from "@/views/IdentityProvidersView.vue";
import { useAuthStore } from "@/stores/auth";

interface ToggleSetting {
  enabled: boolean;
  version: number;
}
interface WebhookSetting extends ToggleSetting {
  path_template: string;
  max_body_bytes: number;
  providers: string[];
  events: string[];
}
interface LockoutSetting extends ToggleSetting {
  max_failures: number;
  window_seconds: number;
}
interface RetentionSetting extends ToggleSetting {
  pipeline_log_days: number;
  audit_log_days: number;
}
interface RuntimeLoggingSetting {
  level: "debug" | "info" | "warn" | "error";
  http_access_enabled: boolean;
  file_enabled: boolean;
  file_directory: string;
  max_file_size_mb: number;
  compress_after_days: number;
  version: number;
}
interface DirectoryUsage {
  path: string;
  files: number;
  bytes: number;
}
interface RuntimeDirectorySetting {
  workspace_directory: string;
  build_directory: string;
  cache_directory: string;
  local_artifact_directory: string;
  version: number;
  workspace_usage: DirectoryUsage;
  build_usage: DirectoryUsage;
  cache_usage: DirectoryUsage;
  artifact_usage: DirectoryUsage;
}
interface DirectoryCleanupReport {
  files_deleted: number;
  bytes_released: number;
  artifacts_expired?: number;
}
interface MigrationStatus {
  id?: string;
  source_driver: string;
  target_driver?: string;
  state: "idle" | "preparing" | "migrating" | "succeeded" | "failed";
  message: string;
  total_tables: number;
  completed_tables: number;
  copied_rows: number;
  requires_restart: boolean;
}
interface TestResult {
  test_token: string;
  driver: string;
  expires_at: string;
  database_created: boolean;
}
type DatabaseDriver = "mysql" | "postgres";
interface DatabaseTargetForm {
  driver: DatabaseDriver;
  host: string;
  port: number | null;
  username: string;
  password: string;
  databaseName: string;
}

const route = useRoute(),
  router = useRouter(),
  auth = useAuthStore(),
  loading = ref(false),
  saving = ref(""),
  passwordSaving = ref(false);
const webhook = ref<WebhookSetting | null>(null),
  lockout = ref<LockoutSetting | null>(null),
  runtimeLogging = ref<RuntimeLoggingSetting | null>(null),
  runtimeDirectories = ref<RuntimeDirectorySetting | null>(null),
  retention = ref<RetentionSetting | null>(null),
  migration = ref<MigrationStatus | null>(null),
  testResult = ref<TestResult | null>(null);
const databaseDefaultPorts: Record<DatabaseDriver, number> = {
  mysql: 3306,
  postgres: 5432,
};
const databaseDefaultUsernames: Record<DatabaseDriver, string> = {
  mysql: "root",
  postgres: "postgres",
};
const password = reactive({ current: "", next: "", confirm: "" }),
  database = reactive<DatabaseTargetForm>({
    driver: "postgres",
    host: "",
    port: databaseDefaultPorts.postgres,
    username: databaseDefaultUsernames.postgres,
    password: "",
    databaseName: "edo",
  });
let migrationTimer = 0;
const canConfig = computed(() => auth.canAny(["config.read"])),
  canUpdate = computed(() => auth.canAny(["config.update"])),
  canExecute = computed(() => auth.canAny(["config.execute"])),
  canIdentity = computed(() => auth.canAny(["identity.read"])),
  isSuperuser = computed(() => Boolean(auth.user?.is_superuser));
const sections = computed(() => [
  { key: "account", label: "修改密码" },
  ...(canConfig.value
    ? [
        { key: "general", label: "安全与接入" },
        { key: "logs", label: "日志设置" },
        { key: "storage", label: "存储目录" },
      ]
    : []),
  ...(isSuperuser.value ? [{ key: "database", label: "数据库迁移" }] : []),
  ...(canIdentity.value ? [{ key: "identity", label: "登录方式" }] : []),
]);
const active = computed(() => (sections.value.some((item) => item.key === route.query.section) ? String(route.query.section) : "account"));
const migrationStatePresentation = computed(
  () =>
    ({
      idle: { label: "未迁移", color: "default" },
      preparing: { label: "准备中", color: "processing" },
      migrating: { label: "迁移中", color: "processing" },
      succeeded: { label: "已完成", color: "success" },
      failed: { label: "迁移失败", color: "error" },
    })[migration.value?.state || "idle"],
);
const migrationInProgress = computed(() => migration.value?.state === "preparing" || migration.value?.state === "migrating");
const migrationProgressPercent = computed(() => {
  const status = migration.value;
  if (!status || status.state === "idle") return 0;
  if (status.state === "succeeded") return 100;
  if (status.total_tables > 0) return Math.min(100, Math.round((status.completed_tables / status.total_tables) * 100));
  return 0;
});
const migrationProgressStatus = computed<"exception" | "success" | "active">(() => {
  if (migration.value?.state === "failed") return "exception";
  if (migration.value?.state === "succeeded") return "success";
  return "active";
});
const migrationStageLabel = computed(() => {
  switch (migration.value?.state) {
    case "preparing":
      return "检查任务并准备目标数据库";
    case "migrating":
      return "正在复制数据表";
    case "succeeded":
      return "迁移完成，等待切换数据库";
    case "failed":
      return "迁移已中止";
    default:
      return "等待开始";
  }
});
function select(value: string) {
  void router.replace({ query: value === "account" ? {} : { section: value } });
}
function databaseDriverLabel(driver?: string) {
  if (driver === "sqlite") return "SQLite";
  if (driver === "mysql") return "MySQL";
  if (driver === "postgres") return "PostgreSQL";
  return "—";
}
async function load() {
  if (!canConfig.value) return;
  loading.value = true;
  try {
    const [a, b, c, d, e] = await Promise.all([client.get<WebhookSetting>("/settings/external-git-webhook"), client.get<LockoutSetting>("/settings/login-lockout"), client.get<RuntimeLoggingSetting>("/settings/runtime-logging"), client.get<RetentionSetting>("/settings/log-retention"), client.get<RuntimeDirectorySetting>("/settings/runtime-directories")]);
    webhook.value = a.data;
    lockout.value = b.data;
    runtimeLogging.value = c.data;
    retention.value = d.data;
    runtimeDirectories.value = e.data;
  } catch (error) {
    message.error(apiErrorMessage(error));
  } finally {
    loading.value = false;
  }
}
async function loadMigration() {
  if (!isSuperuser.value) return;
  try {
    migration.value = (await client.get<MigrationStatus>("/settings/database-migration")).data;
  } catch (error) {
    message.error(apiErrorMessage(error));
  }
}
async function toggle(kind: "webhook" | "lockout") {
  const current = kind === "webhook" ? webhook.value : lockout.value;
  if (!current || !canUpdate.value) return;
  saving.value = kind;
  try {
    const path = kind === "webhook" ? "/settings/external-git-webhook" : "/settings/login-lockout";
    const response = await client.put(path, {
      enabled: !current.enabled,
      expected_version: current.version,
    });
    if (kind === "webhook") webhook.value = response.data;
    else lockout.value = response.data;
    message.success("设置已保存");
  } catch (error) {
    message.error(apiErrorMessage(error));
    await load();
  } finally {
    saving.value = "";
  }
}
async function changePassword() {
  if (password.next.length < 12) {
    message.error("新密码至少需要 12 个字符。");
    return;
  }
  if (password.next !== password.confirm) {
    message.error("两次输入的新密码不一致。");
    return;
  }
  passwordSaving.value = true;
  try {
    await client.put("/auth/password", {
      current_password: password.current,
      new_password: password.next,
    });
    location.assign("/login?reason=password_changed");
  } catch (error) {
    message.error(apiErrorMessage(error));
  } finally {
    passwordSaving.value = false;
  }
}
async function saveRetention() {
  if (!retention.value) return;
  saving.value = "retention";
  try {
    retention.value = (
      await client.put<RetentionSetting>("/settings/log-retention", {
        ...retention.value,
        expected_version: retention.value.version,
      })
    ).data;
    message.success("日志保留策略已保存");
  } catch (error) {
    message.error(apiErrorMessage(error));
    await load();
  } finally {
    saving.value = "";
  }
}
async function saveRuntimeLogging() {
  if (!runtimeLogging.value) return;
  saving.value = "runtime-logging";
  try {
    runtimeLogging.value = (
      await client.put<RuntimeLoggingSetting>("/settings/runtime-logging", {
        level: runtimeLogging.value.level,
        http_access_enabled: runtimeLogging.value.http_access_enabled,
        file_enabled: runtimeLogging.value.file_enabled,
        file_directory: runtimeLogging.value.file_directory,
        max_file_size_mb: runtimeLogging.value.max_file_size_mb,
        compress_after_days: runtimeLogging.value.compress_after_days,
        expected_version: runtimeLogging.value.version,
      })
    ).data;
    message.success("运行日志设置已立即生效");
  } catch (error) {
    message.error(apiErrorMessage(error));
    await load();
  } finally {
    saving.value = "";
  }
}
async function saveRuntimeDirectories() {
  if (!runtimeDirectories.value) return;
  saving.value = "runtime-directories";
  try {
    runtimeDirectories.value = (
      await client.put<RuntimeDirectorySetting>("/settings/runtime-directories", {
        workspace_directory: runtimeDirectories.value.workspace_directory,
        build_directory: runtimeDirectories.value.build_directory,
        cache_directory: runtimeDirectories.value.cache_directory,
        local_artifact_directory: runtimeDirectories.value.local_artifact_directory,
        expected_version: runtimeDirectories.value.version,
      })
    ).data;
    message.success("存储目录已立即生效");
  } catch (error) {
    message.error(apiErrorMessage(error));
    await load();
  } finally {
    saving.value = "";
  }
}
function formatBytes(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / 1024 / 1024).toFixed(1)} MiB`;
  return `${(bytes / 1024 / 1024 / 1024).toFixed(1)} GiB`;
}
function cleanupDirectory(kind: "workspaces" | "builds" | "cache" | "artifacts") {
  if (!canExecute.value) return;
  const options = {
    workspaces: {
      title: "清除仓库工作区？",
      content: "已检出的隔离工作区会被永久删除，后续构建会重新检出代码。当前有构建使用时本次操作会被拒绝。",
      label: "仓库工作区",
    },
    builds: {
      title: "清除构建目录？",
      content: "构建中间输出和残留临时文件会被永久删除。当前有构建使用时本次操作会被拒绝。",
      label: "构建目录",
    },
    cache: {
      title: "清除仓库缓存？",
      content: "Git 对象缓存会被永久删除，后续构建需要重新从远端拉取代码。",
      label: "仓库缓存",
    },
    artifacts: {
      title: "清除本地产物？",
      content: "本地文件产物会被永久删除并标记为已过期，已有构建与发布记录仍会保留。镜像仓库和 Docker 本地镜像不受影响。",
      label: "本地产物",
    },
  }[kind];
  Modal.confirm({
    title: options.title,
    content: options.content,
    okType: "danger",
    okText: "确认清除",
    async onOk() {
      saving.value = `cleanup-${kind}`;
      try {
        const result = (await client.post<DirectoryCleanupReport>(`/settings/runtime-directories/cleanup-${kind}`)).data;
        const expired = result.artifacts_expired ? `，${result.artifacts_expired} 条产物记录已过期` : "";
        message.success(`${options.label}已清除：删除 ${result.files_deleted} 个文件，释放 ${formatBytes(result.bytes_released)}${expired}`);
        await load();
      } catch (error) {
        message.error(apiErrorMessage(error));
        throw error;
      } finally {
        saving.value = "";
      }
    },
  });
}
function viewArtifacts() {
  void router.push({ path: "/build-plans", query: { view: "artifacts" } });
}
function cleanup() {
  if (!canExecute.value) return;
  Modal.confirm({
    title: "立即清理过期日志？",
    content: "超过当前保留时间的流水线日志和审计日志会被永久删除。",
    okType: "danger",
    okText: "立即清理",
    async onOk() {
      saving.value = "cleanup";
      try {
        const result = (
          await client.post<{
            pipeline_logs_deleted: number;
            audit_logs_deleted: number;
          }>("/settings/log-retention/cleanup")
        ).data;
        message.success(`已删除 ${result.pipeline_logs_deleted} 条流水线日志和 ${result.audit_logs_deleted} 条审计日志`);
      } catch (error) {
        message.error(apiErrorMessage(error));
      } finally {
        saving.value = "";
      }
    },
  });
}
function databaseTargetPayload() {
  const host = database.host.trim(),
    username = database.username.trim(),
    databaseName = database.databaseName.trim(),
    port = database.port;
  if (!host || !username || !database.password || !databaseName || port === null || !Number.isInteger(port) || port < 1 || port > 65535) {
    message.error("请完整填写数据库地址、端口、用户名、密码和数据库名。");
    return null;
  }
  return {
    driver: database.driver,
    host,
    port,
    username,
    password: database.password,
    database: databaseName,
  };
}
async function testDatabase() {
  const target = databaseTargetPayload();
  if (!target) return;
  saving.value = "db-test";
  try {
    const result = (await client.post<TestResult>("/settings/database-migration/test", target)).data;
    testResult.value = result;
    message.success(result.database_created ? `已创建目标数据库 ${database.databaseName}，连接测试成功。` : "连接成功，且目标库为空库。测试结果 10 分钟内有效。");
  } catch (error) {
    testResult.value = null;
    message.error(apiErrorMessage(error));
  } finally {
    saving.value = "";
  }
}
async function startMigration() {
  if (!testResult.value) return;
  const target = databaseTargetPayload();
  if (!target) return;
  const testToken = testResult.value.test_token;
  Modal.confirm({
    title: `开始迁移到 ${database.databaseName}？`,
    content: "EDO 将把当前 SQLite 数据快照复制到目标数据库。迁移完成后需要停止当前服务、切换数据库配置并重启。",
    okText: "开始迁移",
    cancelText: "取消",
    async onOk() {
      saving.value = "db-start";
      try {
        migration.value = (
          await client.post<MigrationStatus>("/settings/database-migration", {
            ...target,
            test_token: testToken,
          })
        ).data;
        testResult.value = null;
        message.success("数据库迁移已启动");
      } catch (error) {
        message.error(apiErrorMessage(error));
        throw error;
      } finally {
        saving.value = "";
      }
    },
  });
}
watch(
  () => database.driver,
  (driver, previousDriver) => {
    database.port = databaseDefaultPorts[driver];
    if (!database.username || database.username === databaseDefaultUsernames[previousDriver]) {
      database.username = databaseDefaultUsernames[driver];
    }
  },
);
watch(
  () => [database.driver, database.host, database.port, database.username, database.password, database.databaseName],
  () => {
    testResult.value = null;
  },
);
watch(
  () => migration.value?.state,
  (state) => {
    clearInterval(migrationTimer);
    if (state === "preparing" || state === "migrating") migrationTimer = window.setInterval(loadMigration, 1500);
  },
);
onMounted(() => {
  void load();
  void loadMigration();
});
onBeforeUnmount(() => clearInterval(migrationTimer));
</script>

<template>
  <section>
    <PageToolbar description="管理当前账户、运行目录、日志保留、登录安全和数据库迁移。"
      ><a-button v-if="active === 'general' || active === 'logs' || active === 'storage'" :loading="loading" @click="load"><RefreshCw :size="15" />刷新</a-button></PageToolbar
    >
    <a-segmented :value="active" :options="sections.map((item) => ({ value: item.key, label: item.label }))" class="settings-tabs" @change="(value: string) => select(value)" />
    <article v-if="active === 'account'" class="setting-card vben-card">
      <header>
        <span><KeyRound /></span>
        <div>
          <h3>修改当前账户密码</h3>
          <p>修改成功后，所有设备上的旧会话都会失效，并返回登录界面。</p>
        </div>
      </header>
      <a-form layout="vertical" class="settings-form" @finish="changePassword"
        ><a-form-item label="当前密码" required><a-input-password v-model:value="password.current" autocomplete="current-password" /></a-form-item><a-form-item label="新密码" required><a-input-password v-model:value="password.next" autocomplete="new-password" /></a-form-item><a-form-item label="再次输入新密码" required><a-input-password v-model:value="password.confirm" autocomplete="new-password" /></a-form-item><a-button type="primary" html-type="submit" :loading="passwordSaving">修改密码</a-button></a-form
      >
    </article>
    <template v-if="active === 'general'">
      <article class="setting-card vben-card">
        <header>
          <span><ShieldCheck /></span>
          <div>
            <h3>Git Webhook API</h3>
            <p>允许外部 Git 平台把代码事件发送给 EDO，并继续执行签名校验与投递去重。</p>
          </div>
          <a-switch :checked="webhook?.enabled" :loading="saving === 'webhook'" :disabled="!canUpdate" @change="toggle('webhook')" />
        </header>
        <dl>
          <div>
            <dt>请求地址</dt>
            <dd>
              <code>{{ webhook?.path_template || "/api/v1/webhooks/git/{repository_id}" }}</code>
            </dd>
          </div>
          <div>
            <dt>支持平台</dt>
            <dd>
              {{ webhook?.providers?.join("、") || "GitHub、GitLab、Gitea、Gitee、普通 Git" }}
            </dd>
          </div>
          <div>
            <dt>请求上限</dt>
            <dd>
              {{ Math.round((webhook?.max_body_bytes || 2097152) / 1048576) }}
              MiB
            </dd>
          </div>
        </dl>
      </article>
      <article class="setting-card vben-card">
        <header>
          <span><ShieldCheck /></span>
          <div>
            <h3>登录失败锁定</h3>
            <p>默认关闭；开启后，同一用户名和来源地址连续失败会被暂时锁定。</p>
          </div>
          <a-switch :checked="lockout?.enabled" :loading="saving === 'lockout'" :disabled="!canUpdate" @change="toggle('lockout')" />
        </header>
        <dl>
          <div>
            <dt>触发阈值</dt>
            <dd>{{ lockout?.max_failures || 5 }} 次失败</dd>
          </div>
          <div>
            <dt>锁定时间</dt>
            <dd>{{ Math.round((lockout?.window_seconds || 900) / 60) }} 分钟</dd>
          </div>
          <div>
            <dt>计数维度</dt>
            <dd>用户名与来源地址</dd>
          </div>
        </dl>
      </article>
    </template>
    <template v-if="active === 'storage'">
      <article v-if="runtimeDirectories" class="setting-card vben-card">
        <header>
          <span><FolderCog /></span>
          <div>
            <h3>运行目录</h3>
            <p>仓库工作区、构建中间输出、Git 对象缓存和本地文件产物分开存放，保存后新任务立即使用新目录。</p>
          </div>
        </header>
        <div class="settings-form storage-form">
          <a-form-item label="仓库工作区目录"><a-input v-model:value="runtimeDirectories.workspace_directory" :disabled="!canUpdate" placeholder="/app/data/repositories" /></a-form-item><a-form-item label="构建目录"><a-input v-model:value="runtimeDirectories.build_directory" :disabled="!canUpdate" placeholder="/app/data/builds" /></a-form-item><a-form-item label="Git 缓存目录"><a-input v-model:value="runtimeDirectories.cache_directory" :disabled="!canUpdate" placeholder="/app/data/cache" /></a-form-item><a-form-item label="本地产物目录"><a-input v-model:value="runtimeDirectories.local_artifact_directory" :disabled="!canUpdate" placeholder="/app/data/artifacts" /></a-form-item>
        </div>
        <a-alert type="info" show-icon message="四个目录必须彼此独立，不能互相包含。新目录必须为空或此前已由 EDO 用于相同用途；切换产物目录时会先同步现有本地产物。" />
        <footer>
          <a-button type="primary" :disabled="!canUpdate" :loading="saving === 'runtime-directories'" @click="saveRuntimeDirectories">保存并立即生效</a-button>
        </footer>
      </article>
      <article v-if="runtimeDirectories" class="setting-card vben-card">
        <header>
          <span><Trash2 /></span>
          <div>
            <h3>目录占用与清理</h3>
            <p>显示当前实际生效路径、文件数和占用空间；清理不会删除目录配置。</p>
          </div>
        </header>
        <div class="cleanup-actions">
          <div>
            <div class="directory-meta">
              <strong>仓库工作区</strong><code>{{ runtimeDirectories.workspace_usage.path }}</code>
              <p>{{ runtimeDirectories.workspace_usage.files }} 个文件 · {{ formatBytes(runtimeDirectories.workspace_usage.bytes) }}；存放按仓库和 Commit 隔离的检出代码。</p>
            </div>
            <a-button danger :disabled="!canExecute" :loading="saving === 'cleanup-workspaces'" @click="cleanupDirectory('workspaces')">清除工作区</a-button>
          </div>
          <div>
            <div class="directory-meta">
              <strong>构建目录</strong><code>{{ runtimeDirectories.build_usage.path }}</code>
              <p>{{ runtimeDirectories.build_usage.files }} 个文件 · {{ formatBytes(runtimeDirectories.build_usage.bytes) }}；存放脚本构建中间输出，任务结束后会自动回收。</p>
            </div>
            <a-button danger :disabled="!canExecute" :loading="saving === 'cleanup-builds'" @click="cleanupDirectory('builds')">清除构建</a-button>
          </div>
          <div>
            <div class="directory-meta">
              <strong>Git 缓存</strong><code>{{ runtimeDirectories.cache_usage.path }}</code>
              <p>{{ runtimeDirectories.cache_usage.files }} 个文件 · {{ formatBytes(runtimeDirectories.cache_usage.bytes) }}；清理后后续构建会重新从远端拉取。</p>
            </div>
            <a-button danger :disabled="!canExecute" :loading="saving === 'cleanup-cache'" @click="cleanupDirectory('cache')">清除缓存</a-button>
          </div>
          <div>
            <div class="directory-meta">
              <strong>本地产物</strong><code>{{ runtimeDirectories.artifact_usage.path }}</code>
              <p>{{ runtimeDirectories.artifact_usage.files }} 个文件 · {{ formatBytes(runtimeDirectories.artifact_usage.bytes) }}；构建和手工上传的本地文件产物。</p>
            </div>
            <div class="cleanup-buttons"><a-button @click="viewArtifacts">查看产物</a-button><a-button danger :disabled="!canExecute" :loading="saving === 'cleanup-artifacts'" @click="cleanupDirectory('artifacts')">清除产物</a-button></div>
          </div>
        </div>
      </article>
    </template>
    <template v-if="active === 'logs'">
      <article v-if="runtimeLogging" class="setting-card vben-card">
        <header>
          <span><ScrollText /></span>
          <div>
            <h3>运行日志输出</h3>
            <p>控制结构化日志级别、文件切分和压缩策略，保存后立即生效，不需要重启服务。</p>
          </div>
        </header>
        <div class="settings-form two">
          <a-form-item label="最低输出级别"
            ><a-select
              v-model:value="runtimeLogging.level"
              :disabled="!canUpdate"
              :options="[
                { value: 'debug', label: '调试（Debug）' },
                { value: 'info', label: '信息（Info）' },
                { value: 'warn', label: '警告（Warn）' },
                { value: 'error', label: '错误（Error）' },
              ]" /></a-form-item
          ><a-form-item label="HTTP 访问日志"
            ><div class="switch-field">
              <a-switch v-model:checked="runtimeLogging.http_access_enabled" :disabled="!canUpdate" /><span>{{ runtimeLogging.http_access_enabled ? "记录每个请求的完成状态" : "不记录常规请求完成日志" }}</span>
            </div></a-form-item
          ><a-form-item label="写入滚动日志文件"
            ><div class="switch-field">
              <a-switch v-model:checked="runtimeLogging.file_enabled" :disabled="!canUpdate" /><span>{{ runtimeLogging.file_enabled ? "同时写入本地文件" : "仅输出到标准输出" }}</span>
            </div></a-form-item
          ><a-form-item label="日志目录"><a-input v-model:value="runtimeLogging.file_directory" :disabled="!canUpdate || !runtimeLogging.file_enabled" placeholder="logs" /></a-form-item><a-form-item label="单文件上限（MiB）"><a-input-number v-model:value="runtimeLogging.max_file_size_mb" :min="1" :max="10240" :disabled="!canUpdate || !runtimeLogging.file_enabled" /></a-form-item><a-form-item label="历史日志压缩（天后）"><a-input-number v-model:value="runtimeLogging.compress_after_days" :min="1" :max="3650" :disabled="!canUpdate || !runtimeLogging.file_enabled" /></a-form-item>
        </div>
        <a-alert type="info" show-icon message="活动文件为 edo.log；每天零点或达到大小上限时切分，历史文件带年月日，达到压缩天数后转为 .gz。关闭 HTTP 访问日志不会关闭其他组件的告警和错误日志。" />
        <footer>
          <a-button type="primary" :disabled="!canUpdate" :loading="saving === 'runtime-logging'" @click="saveRuntimeLogging">保存并立即生效</a-button>
        </footer>
      </article>
      <article v-if="retention" class="setting-card vben-card">
        <header>
          <span><Trash2 /></span>
          <div>
            <h3>日志保留与自动清理</h3>
            <p>分别控制流水线执行日志和审计日志的保留时间，新安装默认不自动清理。</p>
          </div>
          <a-switch v-model:checked="retention.enabled" :disabled="!canUpdate" />
        </header>
        <div class="settings-form two">
          <a-form-item label="流水线日志保留天数"><a-input-number v-model:value="retention.pipeline_log_days" :min="1" :max="3650" :disabled="!canUpdate" /></a-form-item><a-form-item label="审计日志保留天数"><a-input-number v-model:value="retention.audit_log_days" :min="1" :max="3650" :disabled="!canUpdate" /></a-form-item>
        </div>
        <a-alert type="warning" show-icon message="立即清理无法撤销，但不会删除流水线运行、发布记录或用户数据。" />
        <footer><a-button danger :disabled="!canExecute || !retention.enabled" :loading="saving === 'cleanup'" @click="cleanup">立即清理</a-button><a-button type="primary" :disabled="!canUpdate" :loading="saving === 'retention'" @click="saveRetention">保存设置</a-button></footer>
      </article>
    </template>
    <article v-if="active === 'database' && isSuperuser" class="setting-card database-migration-card vben-card">
      <header>
        <span><Database /></span>
        <div>
          <h3>SQLite 迁移到 MySQL / PostgreSQL</h3>
          <p>将当前 SQLite 数据快照复制到已创建的空数据库，连接信息不会保存或记录。</p>
        </div>
        <a-tag :color="migrationStatePresentation.color">{{ migrationStatePresentation.label }}</a-tag>
      </header>
      <div class="migration-overview">
        <div>
          <span>当前数据库</span><strong>{{ databaseDriverLabel(migration?.source_driver) }}</strong>
        </div>
        <div>
          <span>目标数据库</span><strong>{{ databaseDriverLabel(migration?.target_driver) }}</strong>
        </div>
        <div>
          <span>迁移进度</span
          ><strong
            >{{ migration?.completed_tables || 0 }} <small>/ {{ migration?.total_tables || 0 }} 表</small></strong
          >
        </div>
        <div>
          <span>已复制数据</span><strong>{{ migration?.copied_rows || 0 }} <small>行</small></strong>
        </div>
      </div>
      <a-alert type="warning" show-icon class="migration-alert" message="迁移完成后必须停止当前服务，切换 EDO_DATABASE_DRIVER / EDO_DATABASE_DSN 并重启；禁止新旧库同时写入。" />
      <section v-if="migration && migration.state !== 'idle'" class="migration-progress-panel" :class="`is-${migration.state}`">
        <div class="migration-progress-heading">
          <div class="migration-progress-title">
            <i :class="{ pulsing: migrationInProgress }"></i>
            <span><strong>{{ migrationStageLabel }}</strong><small>{{ migration.message }}</small></span>
          </div>
          <b>{{ migrationProgressPercent }}%</b>
        </div>
        <a-progress :percent="migrationProgressPercent" :show-info="false" :status="migrationProgressStatus" :stroke-width="8" />
        <div class="migration-progress-meta">
          <span>已完成 <b>{{ migration.completed_tables }}</b> / {{ migration.total_tables }} 张表</span>
          <span>已复制 <b>{{ migration.copied_rows.toLocaleString() }}</b> 行</span>
          <span v-if="migrationInProgress">状态每 1.5 秒自动更新</span>
        </div>
      </section>
      <section class="database-connection-panel">
        <div class="database-panel-heading">
          <div><strong>目标数据库连接</strong><span>数据库不存在时将自动创建，请使用具有创建数据库权限的账户。</span></div>
          <span
            >默认端口 <b>{{ databaseDefaultPorts[database.driver] }}</b></span
          >
        </div>
        <div class="database-target-form">
          <a-form-item class="database-driver-field" label="数据库类型" required
            ><a-select
              v-model:value="database.driver"
              :disabled="migrationInProgress"
              :options="[
                { value: 'mysql', label: 'MySQL 8+' },
                { value: 'postgres', label: 'PostgreSQL 14+' },
              ]" /></a-form-item
          ><a-form-item class="database-host-field" label="地址" required><a-input v-model:value="database.host" :disabled="migrationInProgress" autocomplete="off" placeholder="例如：db.example.com" /></a-form-item><a-form-item class="database-port-field" label="端口" required><a-input-number v-model:value="database.port" :disabled="migrationInProgress" :min="1" :max="65535" :precision="0" :controls="false" /></a-form-item><a-form-item class="database-username-field" label="用户名" required><a-input v-model:value="database.username" :disabled="migrationInProgress" autocomplete="off" placeholder="数据库用户名" /></a-form-item><a-form-item class="database-password-field" label="密码" required><a-input-password v-model:value="database.password" :disabled="migrationInProgress" autocomplete="new-password" placeholder="数据库密码" /></a-form-item><a-form-item class="database-name-field" label="目标数据库名" required><a-input v-model:value="database.databaseName" :disabled="migrationInProgress" autocomplete="off" placeholder="默认 edo；不存在时自动创建" /></a-form-item>
        </div>
        <div class="database-action-bar">
          <div class="database-test-state" :class="{ verified: Boolean(testResult) }">
            <i></i
            ><span
              ><strong>{{ testResult ? "连接已验证" : "等待连接测试" }}</strong
              ><small>{{ testResult ? "测试结果 10 分钟内有效；修改任一连接项后需重新测试。" : "测试连接时会自动创建不存在的目标库；已有目标库必须为空。" }}</small></span
            >
          </div>
          <div class="database-actions"><a-button type="primary" ghost :disabled="migrationInProgress" :loading="saving === 'db-test'" @click="testDatabase">测试连接</a-button><a-button type="primary" :disabled="!testResult || migrationInProgress" :loading="saving === 'db-start' || migrationInProgress" @click="startMigration">{{ migrationInProgress ? "迁移中" : "开始迁移" }}</a-button></div>
        </div>
      </section>
      <p class="migration-message">
        {{ migration?.message || "尚未执行数据库迁移" }}
      </p>
    </article>
    <IdentityProvidersView v-if="active === 'identity'" />
  </section>
</template>

<style scoped>
.settings-tabs {
  margin-bottom: 14px;
}
.setting-card {
  margin-bottom: 14px;
  padding: 21px;
}
.setting-card header {
  display: flex;
  align-items: center;
  gap: 14px;
}
.setting-card header > span:first-child {
  display: grid;
  width: 40px;
  height: 40px;
  place-items: center;
  border-radius: 7px;
  color: var(--edo-primary);
  background: var(--edo-primary-soft);
}
.setting-card header svg {
  width: 20px;
}
.setting-card header > div {
  min-width: 0;
  flex: 1;
}
.setting-card h3,
.setting-card p {
  margin: 0;
}
.setting-card p {
  margin-top: 3px;
  color: var(--edo-muted);
}
.settings-form {
  max-width: 620px;
  margin-top: 20px;
}
.settings-form.two {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 14px;
}
.storage-form {
  display: grid;
  max-width: 920px;
  grid-template-columns: 1fr 1fr;
  gap: 0 16px;
}
.switch-field {
  display: flex;
  min-height: 32px;
  align-items: center;
  gap: 10px;
  color: var(--edo-muted);
}
dl {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 1px;
  margin: 20px 0 0;
  background: var(--edo-border);
}
dl > div {
  padding: 14px;
  background: var(--edo-surface);
}
dt {
  color: var(--edo-muted);
  font-size: 11px;
}
dd {
  margin: 5px 0 0;
}
.setting-card footer {
  display: flex;
  justify-content: flex-end;
  gap: 9px;
  margin-top: 18px;
}
.cleanup-actions {
  margin-top: 18px;
  border-top: 1px solid var(--edo-border);
}
.cleanup-actions > div {
  display: flex;
  align-items: center;
  gap: 18px;
  padding: 15px 0;
  border-bottom: 1px solid var(--edo-border);
}
.cleanup-actions > div > div {
  min-width: 0;
  flex: 1;
}
.cleanup-actions strong {
  font-size: 13px;
}
.cleanup-actions p {
  font-size: 12px;
}
.directory-meta {
  display: grid;
  gap: 4px;
}
.directory-meta code {
  overflow-wrap: anywhere;
  color: var(--edo-text);
  font-size: 12px;
}
.cleanup-buttons {
  display: flex !important;
  flex: 0 0 auto !important;
  gap: 8px;
}
.database-migration-card {
  padding: 24px;
}
.migration-overview {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 1px;
  margin-top: 20px;
  overflow: hidden;
  border: 1px solid var(--edo-border);
  border-radius: 10px;
  background: var(--edo-border);
}
.migration-overview > div {
  min-height: 72px;
  padding: 13px 16px;
  background: var(--edo-surface-soft);
}
.migration-overview span,
.migration-overview strong {
  display: block;
}
.migration-overview span {
  color: var(--edo-muted);
  font-size: 11px;
}
.migration-overview strong {
  margin-top: 5px;
  font-size: 18px;
  font-weight: 650;
}
.migration-overview small {
  color: var(--edo-muted);
  font-size: 12px;
  font-weight: 500;
}
.migration-alert {
  margin: 16px 0;
}
.migration-progress-panel {
  max-width: 1180px;
  margin: 0 auto 16px;
  padding: 16px 18px;
  border: 1px solid color-mix(in srgb, var(--edo-primary) 24%, var(--edo-border));
  border-radius: 12px;
  background: color-mix(in srgb, var(--edo-primary) 5%, var(--edo-surface));
}
.migration-progress-panel.is-succeeded {
  border-color: color-mix(in srgb, #28b66e 30%, var(--edo-border));
  background: color-mix(in srgb, #28b66e 5%, var(--edo-surface));
}
.migration-progress-panel.is-failed {
  border-color: color-mix(in srgb, #ef4444 28%, var(--edo-border));
  background: color-mix(in srgb, #ef4444 4%, var(--edo-surface));
}
.migration-progress-heading,
.migration-progress-title,
.migration-progress-meta {
  display: flex;
  align-items: center;
}
.migration-progress-heading {
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 10px;
}
.migration-progress-title {
  min-width: 0;
  gap: 10px;
}
.migration-progress-title > i {
  width: 9px;
  height: 9px;
  flex: 0 0 9px;
  border-radius: 50%;
  background: var(--edo-primary);
}
.migration-progress-title > i.pulsing {
  animation: migration-pulse 1.5s ease-in-out infinite;
}
.migration-progress-title span,
.migration-progress-title strong,
.migration-progress-title small {
  display: block;
}
.migration-progress-title strong {
  font-size: 13px;
}
.migration-progress-title small {
  margin-top: 2px;
  color: var(--edo-muted);
  font-size: 12px;
}
.migration-progress-heading > b {
  color: var(--edo-primary);
  font-size: 18px;
  font-variant-numeric: tabular-nums;
}
.migration-progress-meta {
  flex-wrap: wrap;
  gap: 8px 20px;
  margin-top: 7px;
  color: var(--edo-muted);
  font-size: 11px;
}
.migration-progress-meta b {
  color: var(--edo-text);
  font-variant-numeric: tabular-nums;
}
@keyframes migration-pulse {
  0%,
  100% {
    box-shadow: 0 0 0 0 color-mix(in srgb, var(--edo-primary) 30%, transparent);
  }
  50% {
    box-shadow: 0 0 0 6px transparent;
  }
}
.database-connection-panel {
  max-width: 1180px;
  margin: 0 auto;
  padding: 18px;
  border: 1px solid var(--edo-border);
  border-radius: 12px;
  background: var(--edo-surface-soft);
}
.database-panel-heading {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 16px;
}
.database-panel-heading strong,
.database-panel-heading span {
  display: block;
}
.database-panel-heading > div > span {
  margin-top: 2px;
  color: var(--edo-muted);
  font-size: 12px;
}
.database-panel-heading > span {
  padding: 4px 9px;
  border: 1px solid var(--edo-border);
  border-radius: 999px;
  color: var(--edo-muted);
  background: var(--edo-surface);
  font-size: 11px;
}
.database-panel-heading b {
  color: var(--edo-text);
}
.database-target-form {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 14px;
}
.database-host-field,
.database-password-field {
  grid-column: span 2;
}
.database-target-form :deep(.ant-form-item) {
  margin-bottom: 0;
}
.database-target-form :deep(.ant-form-item-label) {
  padding-bottom: 5px;
}
.database-target-form :deep(.ant-input-number) {
  width: 100%;
}
.database-action-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 20px;
  margin: 18px -18px -18px;
  padding: 14px 18px;
  border-top: 1px solid var(--edo-border);
  border-radius: 0 0 12px 12px;
  background: var(--edo-surface);
}
.database-test-state {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 10px;
}
.database-test-state > i {
  width: 8px;
  height: 8px;
  flex: 0 0 8px;
  border-radius: 50%;
  background: #a8adb7;
}
.database-test-state.verified > i {
  background: #28b66e;
  box-shadow: 0 0 0 4px color-mix(in srgb, #28b66e 12%, transparent);
}
.database-test-state strong,
.database-test-state small {
  display: block;
}
.database-test-state strong {
  font-size: 12px;
}
.database-test-state small {
  margin-top: 1px;
  color: var(--edo-muted);
  font-size: 11px;
}
.database-actions {
  display: flex;
  flex: 0 0 auto;
  gap: 8px;
}
.migration-message {
  margin: 14px auto 0 !important;
  max-width: 1180px;
  font-size: 12px;
}
@media (max-width: 1050px) {
  .database-target-form {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .database-host-field,
  .database-password-field {
    grid-column: auto;
  }
  .database-action-bar {
    align-items: flex-start;
    flex-direction: column;
  }
  .database-actions {
    width: 100%;
    justify-content: flex-end;
  }
}
@media (max-width: 850px) {
  .migration-overview {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .database-target-form,
  .storage-form {
    grid-template-columns: 1fr;
  }
  .database-panel-heading {
    align-items: flex-start;
    flex-direction: column;
  }
  .database-actions {
    align-items: stretch;
    flex-direction: column;
  }
  .settings-form.two,
  dl {
    grid-template-columns: 1fr;
  }
  .setting-card header {
    align-items: flex-start;
    flex-wrap: wrap;
  }
  .cleanup-actions > div {
    align-items: flex-start;
    flex-direction: column;
  }
  .cleanup-buttons {
    width: 100%;
  }
}
@media (max-width: 520px) {
  .database-migration-card {
    padding: 16px;
  }
  .migration-overview {
    grid-template-columns: 1fr;
  }
  .database-connection-panel {
    padding: 14px;
  }
  .database-action-bar {
    margin: 16px -14px -14px;
    padding: 14px;
  }
  .database-test-state {
    align-items: flex-start;
  }
}
</style>
