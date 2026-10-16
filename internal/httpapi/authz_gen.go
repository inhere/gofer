// 权限参考文档的生成入口（设计「权限参考文档由代码生成」，计划 T1.6）。
//
// The permission reference (docs/reference/permissions.md) is written from code — the
// catalog of internal/authz and the route declarations of authz_routes.go — so that the
// checked-in document can never describe a binary that does not exist:
//
//	go generate ./internal/httpapi/...
//
// rewrites it, and authz_gen_test.go renders the same bytes and fails when the file has
// gone stale (the design's 过期检查, which is what keeps the two in step without a human
// remembering to edit the document).
//
// The document is user-facing: it is what an operator (or an agent reading the docs)
// consults to learn what a permission means and what a route asks for. It therefore
// carries no task numbers, design sections or migration history — only what the running
// code does today (G046).
package httpapi

import (
	"sort"
	"strings"

	"github.com/inhere/gofer/internal/authz"
)

//go:generate go run ./authzgen -out ../../docs/reference/permissions.md

// AuthzReferencePath is the generated permission reference, relative to this package's
// directory (the working directory of both `go generate ./internal/httpapi/...` and of the
// package's own tests, so the generator's -out default and the staleness test share it).
const AuthzReferencePath = "../../docs/reference/permissions.md"

// AuthzReference renders the permission reference: the catalog (name, what it covers,
// risk, whether it may be granted to a session, scope, source) and every route
// declaration (method, path, the user-class category with the permissions it needs, and
// which other credential reaches the route).
//
// It is exported for the two callers that must agree byte for byte: the generator
// (./authzgen, behind `go generate`) and TestAuthzReferenceIsUpToDate. Nothing in the
// request path calls it.
func AuthzReference() string {
	var b strings.Builder
	b.WriteString(referenceIntro)
	writeCatalogSection(&b)
	writeRouteSection(&b)
	return b.String()
}

// referenceIntro is the head of the document: who permissions apply to, how an effective
// permission set is computed, and how to read the two tables.
const referenceIntro = "# 权限参考\n" +
	"\n" +
	"> 生成文件：由 `go generate ./internal/httpapi/...` 从服务端代码写出，请勿手工编辑；\n" +
	"> 改了权限目录或路由声明就重新运行这条命令。\n" +
	"\n" +
	"gofer 的**权限**描述一个**用户类调用方**（人的 CLI、web 控制台、直接连 server 的 agent 会话）能做什么。\n" +
	"worker 用自己的令牌、job 用自己的凭据，两者都不走这套权限，各自受自己的白名单约束（见路由表的「其他凭据」列）。\n" +
	"\n" +
	"## 怎么判定\n" +
	"\n" +
	"- **根管理员**持有全部权限，不受任何开关限制：`server.callers` 里标了 `can_admin: true` 的调用方；\n" +
	"  一个调用方都没声明的部署里，唯一的 `default` 就是根管理员。用 `allow_empty_token` 且没有 callers 的部署没有根管理员。\n" +
	"- 其他用户类调用方的有效权限 = 「来源」为「配置」的那些（每次加载配置重算，只读）\n" +
	"  ∪ 数据库里的授权：发给全体用户类调用方的（首次启用时把「来源」为「种入」的那些发给它们），\n" +
	"  以及发给这个调用方自己的；`gofer auth whoami` 显示当前持有哪些、每项从哪来。\n" +
	"- 授权存的是**模式**（`tunnel.*`、`job.*.others`、`*`）：任何模式都匹配不到 `auth.manage`；通配符不会命中\n" +
	"  「危险」级权限——危险级只能字面写出（例如 `job.submit`），这样以后目录新增危险级权限时，旧的通配符不会悄悄扩大。\n" +
	"- **过滤型**权限（例如 `job.read.others`、`session.read.others`）缺少时不报错，只返回调用方自己的数据；\n" +
	"  其余权限缺少时返回 `403`，响应里指出缺的是哪一项。\n"

// catalogLegend decodes the catalog table's columns that are not self-explanatory.
const catalogLegend = "\n" +
	"「覆盖」是这项权限管到哪些入口与数据。风险：「危险」= 持有者能在 server 或 worker 上执行任意代码，\n" +
	"或读到根管理员的 token；「高」= 影响他人工作、读写敏感内容，或改所有 agent 的输入；「中」「低」依次递减；\n" +
	"「根」只有根管理员持有，任何授权（含通配符）都匹配不到它。\n" +
	"\n" +
	"来源：「种入」= 首次启用时发给全体用户类调用方（保持升级前的行为，以后目录新增这类权限时按版本补发）；\n" +
	"「配置」= 由 `server.governance` 的 `require_answer_capability` / `require_attach_capability` /\n" +
	"`require_tunnel_capability` / `require_admin_capability` 与 `server.callers` 的 `can_answer` / `can_attach` /\n" +
	"`can_tunnel` 能力位推出，只读；「仅根」= 只属于根管理员，与开关无关。\n" +
	"\n" +
	"「授予会话」= 这项权限能不能授予一个终端会话（会话授权只在该会话内生效）；「可限定范围」= 以后能不能限定到某个\n" +
	"项目 / agent / runner，现阶段授权一律作用于全部范围。\n"

// routeLegend explains the declaration categories and the other-credential column of the
// route table.
const routeLegend = "下表是全部路由声明。`声明` 是面向**用户类调用方**的判定方式，`需要的权限` 是这条路由用到的权限名。\n" +
	"少数路由只在对应功能启用时才注册（例如指标、worker 的 WebSocket 握手、presence 收件箱、web 控制台的静态资源）。\n" +
	"\n" +
	"- `Require`：路由门直接判定，缺少列出的任一权限就是 `403`（响应里指出缺哪一项）。\n" +
	"- `InHandler(deny)`：接口按资源归属自己判定——操作他人（或无主）资源时才需要列出的权限，缺了就是 `403`。\n" +
	"- `InHandler(filter)`：同上，但缺权限时不报错，只返回调用方自己的数据。\n" +
	"- `Public`：任何用户类调用方都可以调用，不查权限。\n" +
	"- `NoUserSurface`：没有面向用户类调用方的入口，只能由「其他凭据」列的凭据调用。\n" +
	"\n" +
	"`其他凭据` 列：\n" +
	"\n" +
	"- `job`：job 自己的凭据（job 内的 agent 回读日志、回传结果、写评论等），由 job 凭据白名单按路由放行，与用户权限无关。\n" +
	"- `worker`：worker 用自己的令牌调用的路由（握手、上报、上传与下载内容等）。\n" +
	"- `自身凭据`：这条路由不走用户权限，自己判断凭据——一次性 ticket、schedule 的触发 token、metrics token、\n" +
	"  push 动作 token，或完全公开的探活。\n" +
	"- `静态`：web 控制台的单页与静态资源。\n"

// writeCatalogSection writes the permission catalog, one row per catalog entry, in the
// order the catalog keeps (grouped by resource).
func writeCatalogSection(b *strings.Builder) {
	b.WriteString("\n## 权限目录\n\n")
	b.WriteString("| 权限 | 覆盖 | 风险 | 授予会话 | 可限定范围 | 来源 |\n")
	b.WriteString("|---|---|---|---|---|---|\n")
	for _, p := range authz.Permissions() {
		b.WriteString("| " + markdownCode(p.Name) +
			" | " + markdownCell(p.Description) +
			" | " + p.Risk.Label() +
			" | " + yesNo(p.SessionGrantable) +
			" | " + p.Scope.Label() +
			" | " + p.Source.Label() + " |\n")
	}
	b.WriteString(catalogLegend)
}

// writeRouteSection writes the route table: every declaration of authz_routes.go, sorted
// by path so the entries of one resource are read together.
func writeRouteSection(b *strings.Builder) {
	b.WriteString("\n## 路由与权限\n\n")
	b.WriteString(routeLegend)
	b.WriteString("\n| 方法 | 路径 | 声明 | 需要的权限 | 其他凭据 |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, key := range sortedRouteKeys() {
		decl := routeDecls[key]
		method, path, _ := strings.Cut(key, " ")
		b.WriteString("| " + markdownCode(method) +
			" | " + markdownCode(path) +
			" | " + declCategory(decl) +
			" | " + declPermissions(decl) +
			" | " + declCredentials(decl) + " |\n")
	}
}

// sortedRouteKeys returns the declaration keys sorted by path, then by method, so the
// document is stable (a map's order is not) and routes of one resource stay together.
func sortedRouteKeys() []string {
	keys := make([]string, 0, len(routeDecls))
	for key := range routeDecls {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		mi, pi, _ := strings.Cut(keys[i], " ")
		mj, pj, _ := strings.Cut(keys[j], " ")
		if pi != pj {
			return pi < pj
		}
		return mi < mj
	})
	return keys
}

// declCategory renders the user-class category of a declaration the way the document
// names it, with InHandler's mode — the handler's answer for a caller that does not hold
// the permission.
func declCategory(decl routeDecl) string {
	switch decl.user.kind {
	case userRequire:
		return markdownCode("Require")
	case userInHandler:
		return markdownCode("InHandler(" + string(decl.user.mode) + ")")
	case userPublic:
		return markdownCode("Public")
	case userNone:
		return markdownCode("NoUserSurface")
	case userPending:
		return markdownCode("Pending")
	default:
		return "—"
	}
}

// declPermissions renders the permissions a declaration names, or a dash when it names
// none (Public / NoUserSurface).
func declPermissions(decl routeDecl) string {
	if len(decl.user.perms) == 0 {
		return "—"
	}
	names := make([]string, 0, len(decl.user.perms))
	for _, p := range decl.user.perms {
		names = append(names, markdownCode(p))
	}
	return strings.Join(names, "、")
}

// declCredentials renders the annotations of a declaration: what other credential reaches
// the route besides (or instead of) a user-class caller.
func declCredentials(decl routeDecl) string {
	var parts []string
	if decl.jobCred != "" {
		parts = append(parts, markdownCode("job"))
	}
	if decl.workerSelf {
		parts = append(parts, markdownCode("worker"))
	}
	if decl.external != "" {
		parts = append(parts, markdownCode("自身凭据"))
	}
	if decl.static {
		parts = append(parts, markdownCode("静态"))
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, "、")
}

// markdownCode renders s as an inline code span.
func markdownCode(s string) string { return "`" + s + "`" }

// markdownCell makes a catalog string safe inside a table cell: a `|` would end the cell
// and a newline the row, and a description is prose that may grow either.
func markdownCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.ReplaceAll(s, "\n", " ")
}

// yesNo renders a catalog boolean the way the design table spells it.
func yesNo(v bool) string {
	if v {
		return "是"
	}
	return "否"
}
