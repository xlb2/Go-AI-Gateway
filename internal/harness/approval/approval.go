// Package approval 审批器官：人在回路高危动作挂起状态机。
//
// 定位：harness 解剖图里的"审批升级"（HARNESS-STUDY M6）——模型想干高危事时先挂起，
// 等管理员在终端输入 auth:approve / auth:reject 才执行/取消。
// 只有一个接口 Store（当前 Redis 实现 RedisStore）；换存储 = 新写一个满足接口的类型。
package approval

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"go_im_gateway/internal/harness/runstate"
	"go_im_gateway/internal/harness/sandbox"
)

// rdb 本器官的 Redis 客户端（由 Init 注入，main 启动时调用一次）。
var rdb *redis.Client

// Init 注入 Redis 客户端（main 启动时调用）。
func Init(client *redis.Client) {
	rdb = client
}

// PendingAction 挂起的高危动作。
//
// 关键设计（对齐 codex 的 AskForApproval → Decision → 执行/提权 决策链）：
// 挂起时就要把"批准后到底执行什么"一起带上（一份结构化的 Plan），
// 否则审批通过后无处执行，只能打印一行日志——那审批就是假的。
// 人只负责"批不批"，批完执行什么由这份提案决定，人不能再改（避免审批被当参数注入的通道）。
type PendingAction struct {
	Origin *runstate.Ref   `json:"origin,omitempty"`
	Kind   string          `json:"kind,omitempty"`
	Tool   *ToolInvocation `json:"tool,omitempty"`
	ID     string          `json:"proposal_id,omitempty"`
	// snapshot 只由 GetPending 填充，领取针对实际读取的版本，不能凭字段猜测。
	snapshot string
	owner    uint
	// Action 要执行的动作名称（如 execute_system_defense）
	Action string `json:"action"`
	// Param 动作的参数（如触发防御的情绪）
	Param string `json:"param"`
	// Reason 为什么需要审批（进审计日志，给人看）
	Reason string `json:"reason,omitempty"`
	// Plan 批准后要执行的**结构化执行计划**（argv 或内置动作，见 sandbox.Request）。
	//
	// 为什么不是裸的 Command/Args 字符串：那种字段在诱导调用方"拼一段 shell 命令"，
	// 而命令字符串既是注入面、也是一堆转义坑的来源（本项目踩过一次：
	// cmd.exe 的引号规则把带空格的绝对路径撕开，报"文件名、目录名或卷标语法不正确"）。
	// 计划在挂起时就定死，批准后不再经过模型，也不再有"拼字符串"这一步。
	Plan sandbox.Request `json:"plan"`
	// AvailableDecisions 这个提案允许哪些决策（P2-2）。
	//
	// 为什么做成数据而不是写死 approve/reject：将来会出现"仅本次允许 / 永久允许"
	// 这类真正的选项（codex 的 AvailableDecisions 就是按策略动态生成的），
	// 现在留好位置，到时候只是往数组里加一项，不用改交互协议。
	AvailableDecisions []string `json:"available_decisions,omitempty"`
	// RequestedAt 提案时间（审计用）
	RequestedAt time.Time `json:"requested_at,omitempty"`
	// ExpiresAt 作废时间（= RequestedAt + TTL）。
	//
	// 为什么显式记下来而不是只靠 Redis TTL：TTL 到期后 key 就没了，
	// 用户再敲 auth:approve 只会看到"没有待审批任务" ——
	// 分不清"从没挂起过"和"挂起过但超时了"。记下来才能如实回一句"已超时作废"。
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

const KindTool = "tool"
const KindSandbox = "sandbox"

// ToolInvocation 执行数据与 Param 展示摘要分开，Arguments 不得截断。
type ToolInvocation struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	CallID    string `json:"call_id"`
	UserID    uint   `json:"user_id"`
	RuntimeID string `json:"runtime_id"`
}

// ProposalStore 防止同一用户的第二个工具提案覆盖尚未处理的提案。
type ProposalStore interface {
	Store
	Propose(context.Context, uint, PendingAction) error
}

var ErrPending = errors.New("已有待处理审批，请先批准或拒绝")

// TTL 挂起提案的**有效时长**：超过就作废，批准也不执行。
const TTL = 5 * time.Minute

// redisTTL 实际写进 Redis 的 TTL，比有效时长多留一倍。
//
// 这段"宽限期"是给"已超时作废"这句话留的：如果两者一样，key 到期即消失，
// 就再也说不清那个提案到底存在过没有。
const redisTTL = 2 * TTL

// ErrExpired 挂起的提案已经超时作废（调用方用 errors.Is 判断）。
var ErrExpired = errors.New("审批提案已超时作废")

// Decisions 这个提案允许的决策；提案方没显式给就用默认两个。
func (p PendingAction) Decisions() []string {
	if len(p.AvailableDecisions) > 0 {
		return p.AvailableDecisions
	}
	return []string{"approve", "reject"}
}

// Expired 提案是否已经超时作废。
func (p PendingAction) Expired() bool {
	return !p.ExpiresAt.IsZero() && time.Now().After(p.ExpiresAt)
}

// Remaining 距离作废还剩多久（用于回显；已作废返回 0）。
func (p PendingAction) Remaining() time.Duration {
	if p.ExpiresAt.IsZero() {
		return 0
	}
	if d := time.Until(p.ExpiresAt); d > 0 {
		return d
	}
	return 0
}

// ConfirmCode 两步确认用的短码，由提案内容派生（确定性，不需要额外存储）。
//
// 为什么要它：敲一下 auth:approve 就执行，人完全可以看都不看就批。
// 要求把回显里的码再打一遍，成本极低，却强制人看一眼"到底要执行什么"。
// 码里带了 ExpiresAt，所以重新挂起会换码，旧码自动失效。
func (p PendingAction) ConfirmCode() string {
	if p.ID != "" {
		data, _ := json.Marshal(p)
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:3])
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%d",
		p.Action, p.Param, p.Plan.Summary(), p.ExpiresAt.UnixNano())))
	return hex.EncodeToString(sum[:3]) // 6 个十六进制字符，好念好打
}

// Store 审批器官接口：挂起、读取、清除一个高危动作。
type Store interface {
	SetPending(ctx context.Context, userID uint, action PendingAction) error
	GetPending(ctx context.Context, userID uint) (*PendingAction, error)
	ClearPending(ctx context.Context, userID uint)
}

// RedisStore 是 Store 的 Redis 实现（有效时长 TTL，超时作废；Redis 侧再多留一倍宽限期）。
type RedisStore struct{}

// ClaimStore 原子消费指定读取版本。成功者获得执行/拒绝权；失败者不得执行。
// 领取不是执行成功证明；领取后崩溃不能自动重放副作用。
type ClaimStore interface {
	Store
	ClaimPending(context.Context, uint, PendingAction) (*PendingAction, error)
}

var ErrChanged = errors.New("审批提案已变更或被处理")

// SetPending 把高危动作挂起，并记下作废时间。
func (RedisStore) SetPending(ctx context.Context, userID uint, action PendingAction) error {
	return savePending(ctx, userID, action, false)
}

func (RedisStore) Propose(ctx context.Context, userID uint, action PendingAction) error {
	return savePending(ctx, userID, action, true)
}

func savePending(ctx context.Context, userID uint, action PendingAction, exclusive bool) error {
	ref, err := runstate.Bind(ctx, userID, action.Origin)
	if err != nil {
		return err
	}
	action.Origin = ref
	if rdb == nil {
		return fmt.Errorf("redis client is nil")
	}
	if action.RequestedAt.IsZero() {
		action.RequestedAt = time.Now()
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	action.ID = hex.EncodeToString(id[:])
	if action.ExpiresAt.IsZero() {
		action.ExpiresAt = action.RequestedAt.Add(TTL)
	}
	data, err := json.Marshal(action)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("agent:pending:%d", userID)
	if exclusive {
		// 宽限期内的过期提案可以替换，未过期提案不能覆盖；并发变更使事务失败。
		return rdb.Watch(ctx, func(tx *redis.Tx) error {
			previous, err := tx.Get(ctx, key).Bytes()
			if err != nil && err != redis.Nil {
				return err
			}
			if err == nil {
				var old PendingAction
				if err := json.Unmarshal(previous, &old); err != nil {
					return err
				}
				if !old.Expired() {
					return ErrPending
				}
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.Set(ctx, key, data, redisTTL)
				return nil
			})
			return err
		}, key)
	}
	return rdb.Set(ctx, key, data, redisTTL).Err()
}

// GetPending 读取当前挂起的高危动作。
//
// 没有挂起 → (nil, nil)，不算错误；
// 已经超时作废 → (nil, ErrExpired)，让调用方能明确区分这两种情况 ——
// 这是 P2-2 要修的：以前超时是 Redis TTL 隐式发生的，用户敲 auth:approve
// 只会得到"没有待审批任务"，人还以为提案还挂着。
func (RedisStore) GetPending(ctx context.Context, userID uint) (*PendingAction, error) {
	if rdb == nil {
		return nil, fmt.Errorf("redis client is nil")
	}
	key := fmt.Sprintf("agent:pending:%d", userID)
	data, err := rdb.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var action PendingAction
	if err := json.Unmarshal(data, &action); err != nil {
		return nil, err
	}
	if action.Expired() {
		return nil, fmt.Errorf("%w（挂起于 %s，有效期 %s）",
			ErrExpired, action.RequestedAt.Format(time.RFC3339), TTL)
	}
	action.snapshot = string(data)
	action.owner = userID
	return &action, nil
}

var claimScript = redis.NewScript(`
local current = redis.call('GET', KEYS[1])
if not current or current ~= ARGV[1] then return 0 end
local deadline = tonumber(ARGV[2])
local now = redis.call('TIME')
local millis = tonumber(now[1]) * 1000 + math.floor(tonumber(now[2]) / 1000)
if deadline > 0 and millis >= deadline then return -1 end
if redis.call('HSETNX', KEYS[2], ARGV[3], ARGV[4]) ~= 1 then return 0 end
redis.call('DEL', KEYS[1])
return 1
`)

func (RedisStore) ClaimPending(ctx context.Context, userID uint, expected PendingAction) (*PendingAction, error) {
	if rdb == nil {
		return nil, fmt.Errorf("redis client is nil")
	}
	if expected.snapshot == "" || expected.owner != userID {
		return nil, ErrChanged
	}
	// 返回存储中的原提案，调用方修改 expected 的展示字段不能改变执行内容。
	var action PendingAction
	if err := json.Unmarshal([]byte(expected.snapshot), &action); err != nil {
		return nil, err
	}
	var deadline int64
	if !action.ExpiresAt.IsZero() {
		deadline = action.ExpiresAt.UnixMilli()
	}
	key := fmt.Sprintf("agent:pending:%d", userID)
	if action.ID == "" {
		sum := sha256.Sum256([]byte(expected.snapshot))
		action.ID = hex.EncodeToString(sum[:])
	}
	record, err := json.Marshal(Execution{Proposal: action, State: Claimed, UpdatedAt: time.Now().UTC()})
	if err != nil {
		return nil, err
	}
	result, err := claimScript.Run(ctx, rdb, []string{key, executionKey(userID)}, expected.snapshot, deadline, action.ID, string(record)).Int()
	if err != nil {
		return nil, fmt.Errorf("领取审批结果未确认，禁止执行: %w", err)
	}
	switch result {
	case 1:
		return &action, nil
	case -1:
		return nil, ErrExpired
	default:
		return nil, ErrChanged
	}
}

// ClearPending 清除挂起状态（审批完无论通过还是拒绝都清掉）。
func (RedisStore) ClearPending(ctx context.Context, userID uint) {
	if rdb == nil {
		return
	}
	key := fmt.Sprintf("agent:pending:%d", userID)
	rdb.Del(ctx, key)
}
