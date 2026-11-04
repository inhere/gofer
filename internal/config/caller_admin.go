// 用户管理的入参、结果与错误（配置入库 P1，计划 T6）：入口层（httpapi 的 `/v1/callers*`）与组装层
// （core 的 CallerAdmin 实现）都要用同一组「新增 / 轮换 / 停用 / 启用 / 改上限 / 改配额」的类型，而
// 入口层不 import core（G022：入口 → 编排 → 业务 → 数据层），所以它们和调用方的模型一起放在
// config。
//
// 这里只有值类型、常量与判定，没有业务逻辑：写入顺序、token 生成、审计在 core 的 CallerAdmin
// 方法里，错误到 HTTP 状态的映射在 httpapi。
package config

import (
	"errors"
	"fmt"
	"strings"

	yaml "github.com/goccy/go-yaml"
)

// 调用方的来源与状态（列表视图用，wire 上就是这两个字段的字符串）。
const (
	// CallerSourceFile is a caller declared in the configuration file (the operator's own
	// entry). The zero value of CallerConfig.Source means the same thing.
	CallerSourceFile = "file"
	// CallerStateActive: the caller is in force (it may authenticate).
	CallerStateActive = "active"
	// CallerStateDisabled: a stored caller switched off — it authenticates nothing, and no
	// new work of its owner starts.
	CallerStateDisabled = "disabled"
	// CallerStateSuperseded: a stored caller a same-id configuration-file entry (or a worker
	// binding) has taken over: the file wins, the row stays visible so an operator can rotate
	// and enable it again after the file entry is gone.
	CallerStateSuperseded = "superseded"
)

// CallerSpec is the input of `caller add` (POST /v1/callers): the id, the optional display
// name, the quota overrides and the ceiling. A ceiling is REQUIRED in one of its two forms —
// ceil either lists what the caller may do or NoCeiling says 「no limit, on purpose」 (D16: an
// empty ceiling block would read as a limit while restricting nothing).
type CallerSpec struct {
	ID   string
	Name string
	// MaxConcurrentJobs / RateLimit / RateBurst are the per-caller quota overrides (0 = take
	// the server.governance default, which itself may be unlimited).
	MaxConcurrentJobs int
	RateLimit         float64
	RateBurst         int
	// Ceiling is the caller's upper bound; nil means none, and NoCeiling has to say so.
	Ceiling *CallerCeiling
	// NoCeiling states that the caller gets NO ceiling on purpose (the opposite of leaving the
	// ceiling out, which is an error).
	NoCeiling bool
}

// CallerView is one caller as the management routes report it: where it comes from, whether it
// is in force, the credential it holds (a hint, never the token) and what it is limited to.
type CallerView struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// Source is CallerSourceFile ("" is rendered as file too) or CallerSourceDB.
	Source string `json:"source"`
	// State is active | disabled | superseded.
	State    string `json:"state"`
	TokenSet bool   `json:"token_set"`
	// TokenHint is `gct_…<last four>` for a stored caller: enough to tell two tokens apart,
	// never enough to use one.
	TokenHint         string         `json:"token_hint,omitempty"`
	MaxConcurrentJobs int            `json:"max_concurrent_jobs,omitempty"`
	RateLimit         float64        `json:"rate_limit,omitempty"`
	RateBurst         int            `json:"rate_burst,omitempty"`
	Ceiling           *CallerCeiling `json:"ceiling,omitempty"`
	CreatedBy         string         `json:"created_by,omitempty"`
	CreatedAt         int64          `json:"created_at,omitempty"`
	RotatedAt         int64          `json:"rotated_at,omitempty"`
	UpdatedAt         int64          `json:"updated_at,omitempty"`
	Warnings          []string       `json:"warnings,omitempty"`
}

// CallerChange is the answer of a caller write: the caller as it is now, the token when the
// write MINTED one (add / rotate — shown in this response only, never stored or logged), and
// the notices the write produced (a ceiling that is not a security boundary, a failed audit
// line, a caller left without a ceiling).
type CallerChange struct {
	Caller   CallerView `json:"caller"`
	Token    string     `json:"token,omitempty"`
	Warnings []string   `json:"warnings,omitempty"`
}

// 用户管理写入的错误分类。httpapi 只按它们映射状态码，不看错误文本。
var (
	// ErrCallerNotFound: no such caller (404).
	ErrCallerNotFound = errors.New("caller not found")
	// ErrCallerFileSource: the id belongs to a configuration-file caller, which this API may
	// not rotate, disable or enable — only edit (409).
	ErrCallerFileSource = errors.New("the caller is declared in the configuration file")
	// ErrCallerIDTaken: the id is already used by another caller, a file caller or a worker
	// binding. Ids are never reused (409).
	ErrCallerIDTaken = errors.New("the caller id is already in use")
	// ErrCallerRotationRequired: a caller taken over by a file entry may only be enabled after
	// its token was rotated (409), so a token from before the takeover cannot come back.
	ErrCallerRotationRequired = errors.New("rotate the caller's token before enabling it")
	// ErrCallerInvalid wraps a rejected input (400).
	ErrCallerInvalid = errors.New("invalid caller")
)

// CallerInputErrorf builds a 400-class rejection that names what is wrong.
func CallerInputErrorf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrCallerInvalid, fmt.Sprintf(format, a...))
}

// CanonicalCallerCeiling renders a ceiling for storage and validates it by the SAME rules a
// stored ceiling is validated with (D16 + D17): the block is decoded strictly (an unknown key
// is a typo that would otherwise read as 「no such restriction」), every glob must be legal,
// and a block that sets no field at all is refused — 「no ceiling」 is the ABSENCE of the block,
// not an empty one.
//
// nil (no ceiling) answers ("", nil, nil). The canonical paragraph is what both the database
// (ceiling_yaml) and the file (server.callers[].ceiling) store.
func CanonicalCallerCeiling(ceil *CallerCeiling) (string, *CallerCeiling, error) {
	if ceil == nil {
		return "", nil, nil
	}
	rendered, err := yaml.Marshal(ceil)
	if err != nil {
		return "", nil, CallerInputErrorf("ceiling: %v", err)
	}
	parsed, err := DecodeCallerCeiling(string(rendered))
	if err != nil {
		return "", nil, CallerInputErrorf("%v", err)
	}
	text, err := yaml.Marshal(parsed)
	if err != nil {
		return "", nil, CallerInputErrorf("ceiling: %v", err)
	}
	return string(text), parsed, nil
}

// CallerLimitsOf projects the quota fields of a spec (or of a caller entry) onto the stored
// quota block.
func CallerLimitsOf(maxConcurrent int, rateLimit float64, rateBurst int) CallerLimits {
	return CallerLimits{MaxConcurrentJobs: maxConcurrent, RateLimit: rateLimit, RateBurst: rateBurst}
}

// ValidateCallerLimits holds a quota block to the rule a stored one is decoded with: no field
// may be negative.
func ValidateCallerLimits(lim CallerLimits) error {
	if lim.MaxConcurrentJobs < 0 {
		return CallerInputErrorf("max_concurrent_jobs must not be negative")
	}
	if lim.RateLimit < 0 {
		return CallerInputErrorf("rate_limit must not be negative")
	}
	if lim.RateBurst < 0 {
		return CallerInputErrorf("rate_burst must not be negative")
	}
	return nil
}

// ValidCallerTokenHash holds a stored hash to the form an ENABLED caller must have: 64 hex
// digits. A row whose hash is damaged cannot be enabled (it would authenticate nothing usable);
// the row has to be rotated first.
func ValidCallerTokenHash(h string) error {
	if !isHexSHA256(strings.TrimSpace(h)) {
		return CallerInputErrorf("the stored token hash is not a 64-character hex sha256: rotate the token")
	}
	return nil
}

// CallerCeilingWarning is the per-caller form of the load-time ceiling notice (D13): the
// ceiling lets this caller use the machine runner, which is not a security boundary because a
// job there runs on the server host. It is what `caller add` answers in `warnings` and what the
// load reports for the same entry.
func CallerCeilingWarning(label string, cc CallerConfig) (string, bool) {
	return callerCeilingWarning(label, cc)
}

// CallerSourceLabel renders the source of a caller for a view: the empty yaml source is the
// configuration file.
func CallerSourceLabel(source string) string {
	if strings.TrimSpace(source) == "" {
		return CallerSourceFile
	}
	return source
}
