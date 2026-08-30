package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	dbgen "github.com/oratis/babelplus/api/db/gen"
	"github.com/oratis/babelplus/api/internal/audit"
	"github.com/oratis/babelplus/api/internal/gen"
	"github.com/oratis/babelplus/api/internal/middleware"
)

// 管理面「目录与运营」这一组的测试。
//
// 与 order_test.go / task_test.go 同一条纪律：测**纯函数**与**吃窄接口的自由函数**，
// 不测 handler 方法（Server.db 是具体类型 *store.Store，塞不了假实现）。
// handler 这一层只断言「确实实现了」/「确实仍是 501」。
//
// 本文件里带 🔴 的用例都是「不这么做会**静默**出错」的那一类：
//   - 建套餐漏 kind → 加油包被写成周期套餐，只在有人下单时才显形
//   - 优惠码 percent 的 bps 换算 → 少乘 100 = 用户几乎没优惠，多乘 = 白送
//   - 审计过滤用等值匹配 → 一条都查不到，且不报错
//   - 内部备注推进 SLA 首次响应时钟 → 告警系统开始撒谎，且往好看的方向
//   - 统计的时区换算 → 每天的数字都是隔壁那天的
//
// 每条危险操作还有两个必测用例（任务书要求）：
//   - 「参数没收齐时不许提交」（reason < 8 字符 / 权限位不足 / 缺必填项）
//   - 「审计写失败则业务回滚」

// ============================================================
// 测试替身
// ============================================================

// fakeCatalogTx 是事务体里那个 dbgen.Querier 的假实现。
//
// 🔴 **内嵌 dbgen.Querier（值为 nil）而不是把 200 多个方法写全**：
// 用例调到没有覆盖的查询时会 panic 并指出方法名 —— 那正是
// 「这个 operation 悄悄多跑了一条查询」的信号，比静默返回零值好得多。
type fakeCatalogTx struct {
	dbgen.Querier

	// 套餐
	createPlanArg  *dbgen.CreatePlanParams
	createPlanRow  dbgen.Plan
	createPlanErrs []error // 每次调用弹出一个（模拟撞码重试）
	getPlanRow     dbgen.AdminGetPlanForUpdateRow
	getPlanErr     error
	updatePlanArg  *dbgen.AdminUpdatePlanParams
	updatePlanRow  dbgen.AdminUpdatePlanRow
	updatePlanErr  error
	archivePlanRow dbgen.AdminArchivePlanRow
	archivePlanErr error

	// 优惠码
	createCouponArg *dbgen.AdminCreateCouponParams
	createCouponRow dbgen.Coupon
	createCouponErr error
	getCouponRow    dbgen.AdminGetCouponForUpdateRow
	getCouponErr    error
	updateCouponArg *dbgen.AdminUpdateCouponParams
	updateCouponRow dbgen.AdminUpdateCouponRow
	updateCouponErr error
	deleteCouponRow dbgen.Coupon
	deleteCouponErr error

	// 公告
	createNoticeArg *dbgen.CreateAdminNoticeParams
	createNoticeRow dbgen.CreateAdminNoticeRow
	createNoticeErr error
	updateNoticeArg *dbgen.UpdateAdminNoticeParams
	updateNoticeRow dbgen.UpdateAdminNoticeRow
	updateNoticeErr error
	deleteNoticeRow dbgen.Notice
	deleteNoticeErr error

	// 邀请
	inviteBatches [][]string
	inviteRows    [][]dbgen.InviteCode
	inviteErr     error

	// 佣金
	commissionArg *dbgen.AdjustAdminCommissionAmountParams
	commissionRow dbgen.AdjustAdminCommissionAmountRow
	commissionErr error

	// 工单
	updateTicketArg *dbgen.AdminUpdateTicketParams
	updateTicketRow dbgen.AdminUpdateTicketRow
	updateTicketErr error
	bumpArg         *dbgen.AdminBumpTicketOnAgentMessageParams
	bumpRow         dbgen.AdminBumpTicketOnAgentMessageRow
	bumpErr         error
	msgArg          *dbgen.CreateTicketMessageParams
	msgRow          dbgen.TicketMessage
	msgErr          error

	// 配置
	settingsArg  *dbgen.UpdateAdminSettingsValuesParams
	settingsRows []dbgen.UpdateAdminSettingsValuesRow
	settingsErr  error

	// 群发
	audienceArg   *dbgen.AdminCountBroadcastAudienceParams
	audienceCount int64
	audienceErr   error
	enqueueArg    *dbgen.AdminEnqueueBroadcastMailsParams
	enqueueRows   []dbgen.AdminEnqueueBroadcastMailsRow
	enqueueErr    error
}

func (f *fakeCatalogTx) CreatePlan(_ context.Context, arg dbgen.CreatePlanParams) (dbgen.Plan, error) {
	a := arg
	f.createPlanArg = &a
	if len(f.createPlanErrs) > 0 {
		err := f.createPlanErrs[0]
		f.createPlanErrs = f.createPlanErrs[1:]
		if err != nil {
			return dbgen.Plan{}, err
		}
	}
	row := f.createPlanRow
	row.Code = arg.Code
	row.Kind = arg.Kind
	row.Name = arg.Name
	row.GroupID = arg.GroupID
	row.TransferEnable = arg.TransferEnable
	row.DeviceLimit = arg.DeviceLimit
	row.SpeedLimitMbps = arg.SpeedLimitMbps
	row.PriceMonthly = arg.PriceMonthly
	row.PriceYearly = arg.PriceYearly
	row.PriceOnetime = arg.PriceOnetime
	row.Visible = arg.Visible
	row.Sellable = arg.Sellable
	row.ContentMd = arg.ContentMd
	row.SortOrder = arg.SortOrder
	row.ResetTrafficMethod = arg.ResetTrafficMethod
	return row, nil
}

func (f *fakeCatalogTx) AdminGetPlanForUpdate(_ context.Context, _ int64) (dbgen.AdminGetPlanForUpdateRow, error) {
	return f.getPlanRow, f.getPlanErr
}

func (f *fakeCatalogTx) AdminUpdatePlan(_ context.Context, arg dbgen.AdminUpdatePlanParams) (dbgen.AdminUpdatePlanRow, error) {
	a := arg
	f.updatePlanArg = &a
	return f.updatePlanRow, f.updatePlanErr
}

func (f *fakeCatalogTx) AdminArchivePlan(_ context.Context, _ int64) (dbgen.AdminArchivePlanRow, error) {
	return f.archivePlanRow, f.archivePlanErr
}

func (f *fakeCatalogTx) AdminCreateCoupon(_ context.Context, arg dbgen.AdminCreateCouponParams) (dbgen.Coupon, error) {
	a := arg
	f.createCouponArg = &a
	if f.createCouponErr != nil {
		return dbgen.Coupon{}, f.createCouponErr
	}
	row := f.createCouponRow
	row.Code = strings.ToUpper(arg.Code)
	row.Type = arg.Type
	row.Value = arg.Value
	row.ScopePlanIds = arg.ScopePlanIds
	row.TotalUses = arg.TotalUses
	row.StartsAt = arg.StartsAt
	row.EndsAt = arg.EndsAt
	row.Visible = arg.Visible
	return row, nil
}

func (f *fakeCatalogTx) AdminGetCouponForUpdate(_ context.Context, _ int64) (dbgen.AdminGetCouponForUpdateRow, error) {
	return f.getCouponRow, f.getCouponErr
}

func (f *fakeCatalogTx) AdminUpdateCoupon(_ context.Context, arg dbgen.AdminUpdateCouponParams) (dbgen.AdminUpdateCouponRow, error) {
	a := arg
	f.updateCouponArg = &a
	return f.updateCouponRow, f.updateCouponErr
}

func (f *fakeCatalogTx) AdminDeleteCoupon(_ context.Context, _ int64) (dbgen.Coupon, error) {
	return f.deleteCouponRow, f.deleteCouponErr
}

func (f *fakeCatalogTx) CreateAdminNotice(_ context.Context, arg dbgen.CreateAdminNoticeParams) (dbgen.CreateAdminNoticeRow, error) {
	a := arg
	f.createNoticeArg = &a
	if f.createNoticeErr != nil {
		return dbgen.CreateAdminNoticeRow{}, f.createNoticeErr
	}
	row := f.createNoticeRow
	row.Title = arg.Title
	row.ContentMd = arg.ContentMd
	row.Pinned = arg.Pinned
	row.StartsAt = arg.StartsAt
	return row, nil
}

func (f *fakeCatalogTx) UpdateAdminNotice(_ context.Context, arg dbgen.UpdateAdminNoticeParams) (dbgen.UpdateAdminNoticeRow, error) {
	a := arg
	f.updateNoticeArg = &a
	return f.updateNoticeRow, f.updateNoticeErr
}

func (f *fakeCatalogTx) DeleteAdminNotice(_ context.Context, _ int64) (dbgen.Notice, error) {
	return f.deleteNoticeRow, f.deleteNoticeErr
}

func (f *fakeCatalogTx) CreateAdminInviteCodes(_ context.Context, arg dbgen.CreateAdminInviteCodesParams) ([]dbgen.InviteCode, error) {
	f.inviteBatches = append(f.inviteBatches, arg.Codes)
	if f.inviteErr != nil {
		return nil, f.inviteErr
	}
	if len(f.inviteRows) == 0 {
		// 默认：全部成功。
		out := make([]dbgen.InviteCode, 0, len(arg.Codes))
		for i, c := range arg.Codes {
			out = append(out, dbgen.InviteCode{ID: int64(i + 1), Code: c, MaxUses: arg.MaxUses})
		}
		return out, nil
	}
	rows := f.inviteRows[0]
	f.inviteRows = f.inviteRows[1:]
	return rows, nil
}

func (f *fakeCatalogTx) AdjustAdminCommissionAmount(_ context.Context, arg dbgen.AdjustAdminCommissionAmountParams) (dbgen.AdjustAdminCommissionAmountRow, error) {
	a := arg
	f.commissionArg = &a
	return f.commissionRow, f.commissionErr
}

func (f *fakeCatalogTx) AdminUpdateTicket(_ context.Context, arg dbgen.AdminUpdateTicketParams) (dbgen.AdminUpdateTicketRow, error) {
	a := arg
	f.updateTicketArg = &a
	return f.updateTicketRow, f.updateTicketErr
}

func (f *fakeCatalogTx) AdminBumpTicketOnAgentMessage(_ context.Context, arg dbgen.AdminBumpTicketOnAgentMessageParams) (dbgen.AdminBumpTicketOnAgentMessageRow, error) {
	a := arg
	f.bumpArg = &a
	return f.bumpRow, f.bumpErr
}

func (f *fakeCatalogTx) CreateTicketMessage(_ context.Context, arg dbgen.CreateTicketMessageParams) (dbgen.TicketMessage, error) {
	a := arg
	f.msgArg = &a
	if f.msgErr != nil {
		return dbgen.TicketMessage{}, f.msgErr
	}
	row := f.msgRow
	row.Body = arg.Body
	row.IsInternal = arg.IsInternal
	row.ActorType = arg.ActorType
	return row, nil
}

func (f *fakeCatalogTx) UpdateAdminSettingsValues(_ context.Context, arg dbgen.UpdateAdminSettingsValuesParams) ([]dbgen.UpdateAdminSettingsValuesRow, error) {
	a := arg
	f.settingsArg = &a
	return f.settingsRows, f.settingsErr
}

func (f *fakeCatalogTx) AdminCountBroadcastAudience(_ context.Context, arg dbgen.AdminCountBroadcastAudienceParams) (int64, error) {
	a := arg
	f.audienceArg = &a
	return f.audienceCount, f.audienceErr
}

func (f *fakeCatalogTx) AdminEnqueueBroadcastMails(_ context.Context, arg dbgen.AdminEnqueueBroadcastMailsParams) ([]dbgen.AdminEnqueueBroadcastMailsRow, error) {
	a := arg
	f.enqueueArg = &a
	return f.enqueueRows, f.enqueueErr
}

// fakeAuditRun 是 catalogAuditRunner 的假实现。
//
// 它把 audit.InTx 的两条语义都还原出来：
//   - fn 返回错误 → 不提交（业务写回滚）；
//   - fn 成功但**审计写失败** → 同样不提交（§6.3 第 1 条）。
type fakeAuditRun struct {
	tx *fakeCatalogTx
	// auditErr 非 nil 表示模拟「审计写失败」。
	auditErr error

	calls     int
	committed bool
	entries   []audit.Entry
	actor     audit.Actor
}

func (f *fakeAuditRun) runner() catalogAuditRunner {
	return func(ctx context.Context, actor audit.Actor,
		fn func(context.Context, dbgen.Querier) (audit.Entry, error),
	) error {
		f.calls++
		f.actor = actor
		entry, err := fn(ctx, f.tx)
		if err != nil {
			return err
		}
		if f.auditErr != nil {
			// 业务写已经发生在事务里，但事务不会提交 —— 对外等价于什么都没做。
			return f.auditErr
		}
		f.entries = append(f.entries, entry)
		f.committed = true
		return nil
	}
}

func (f *fakeAuditRun) lastEntry(t *testing.T) audit.Entry {
	t.Helper()
	if len(f.entries) == 0 {
		t.Fatal("没有写出任何审计条目 —— 管理面的写操作必须留痕（api-contract §6.3）")
	}
	return f.entries[len(f.entries)-1]
}

func testActor() audit.Actor {
	return audit.Actor{
		AdminID: 7,
		Email:   "ops@example.com",
		IP:      netip.MustParseAddr("203.0.113.9"),
	}
}

func pgErr(code string) error { return &pgconn.PgError{Code: code} }

func collectWarn(dst *[]string) func(string) {
	return func(msg string) { *dst = append(*dst, msg) }
}

func hasSubstr(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// ============================================================
// 本组 operation 的落点：实现的必须实现，501 的必须还是 501
// ============================================================

func TestAdminCatalogOperationsAreImplemented(t *testing.T) {
	var s any = &Server{}
	if _, ok := s.(interface {
		ListAdminPlans(context.Context, gen.ListAdminPlansRequestObject) (gen.ListAdminPlansResponseObject, error)
		CreateAdminPlan(context.Context, gen.CreateAdminPlanRequestObject) (gen.CreateAdminPlanResponseObject, error)
		UpdateAdminPlan(context.Context, gen.UpdateAdminPlanRequestObject) (gen.UpdateAdminPlanResponseObject, error)
		DeleteAdminPlan(context.Context, gen.DeleteAdminPlanRequestObject) (gen.DeleteAdminPlanResponseObject, error)
		ListAdminCoupons(context.Context, gen.ListAdminCouponsRequestObject) (gen.ListAdminCouponsResponseObject, error)
		CreateAdminCoupon(context.Context, gen.CreateAdminCouponRequestObject) (gen.CreateAdminCouponResponseObject, error)
		UpdateAdminCoupon(context.Context, gen.UpdateAdminCouponRequestObject) (gen.UpdateAdminCouponResponseObject, error)
		DeleteAdminCoupon(context.Context, gen.DeleteAdminCouponRequestObject) (gen.DeleteAdminCouponResponseObject, error)
		ListAdminNotices(context.Context, gen.ListAdminNoticesRequestObject) (gen.ListAdminNoticesResponseObject, error)
		CreateAdminNotice(context.Context, gen.CreateAdminNoticeRequestObject) (gen.CreateAdminNoticeResponseObject, error)
		UpdateAdminNotice(context.Context, gen.UpdateAdminNoticeRequestObject) (gen.UpdateAdminNoticeResponseObject, error)
		DeleteAdminNotice(context.Context, gen.DeleteAdminNoticeRequestObject) (gen.DeleteAdminNoticeResponseObject, error)
		ListAdminInvites(context.Context, gen.ListAdminInvitesRequestObject) (gen.ListAdminInvitesResponseObject, error)
		CreateAdminInvite(context.Context, gen.CreateAdminInviteRequestObject) (gen.CreateAdminInviteResponseObject, error)
		AdjustAdminCommission(context.Context, gen.AdjustAdminCommissionRequestObject) (gen.AdjustAdminCommissionResponseObject, error)
		ListAdminTickets(context.Context, gen.ListAdminTicketsRequestObject) (gen.ListAdminTicketsResponseObject, error)
		GetAdminTicket(context.Context, gen.GetAdminTicketRequestObject) (gen.GetAdminTicketResponseObject, error)
		UpdateAdminTicket(context.Context, gen.UpdateAdminTicketRequestObject) (gen.UpdateAdminTicketResponseObject, error)
		CreateAdminTicketMessage(context.Context, gen.CreateAdminTicketMessageRequestObject) (gen.CreateAdminTicketMessageResponseObject, error)
		ListAdminAuditLog(context.Context, gen.ListAdminAuditLogRequestObject) (gen.ListAdminAuditLogResponseObject, error)
		GetAdminSettings(context.Context, gen.GetAdminSettingsRequestObject) (gen.GetAdminSettingsResponseObject, error)
		UpdateAdminSettings(context.Context, gen.UpdateAdminSettingsRequestObject) (gen.UpdateAdminSettingsResponseObject, error)
		GetAdminStats(context.Context, gen.GetAdminStatsRequestObject) (gen.GetAdminStatsResponseObject, error)
		ExportAdminStats(context.Context, gen.ExportAdminStatsRequestObject) (gen.ExportAdminStatsResponseObject, error)
		GetAdminDashboard(context.Context, gen.GetAdminDashboardRequestObject) (gen.GetAdminDashboardResponseObject, error)
		BroadcastAdminMail(context.Context, gen.BroadcastAdminMailRequestObject) (gen.BroadcastAdminMailResponseObject, error)
		ListAdminMailLogs(context.Context, gen.ListAdminMailLogsRequestObject) (gen.ListAdminMailLogsResponseObject, error)
	}); !ok {
		t.Fatal("目录与运营这一组里有 operation 没有被 Server 覆盖，仍落在 Unimplemented 的 501 上")
	}
}

// 🔴 五个缺表的 operation **必须**还是 501。
//
// 把它们实现成「返回一份写死在 Go 里的清单」或者「塞进 settings 的 JSONB」
// 会得到一个看起来能用、背后什么都没有的后台页面 —— 比 501 糟得多。
// 这个用例是那条裁决的执行者：谁哪天顺手实现了它们，这里会红。
func TestAdminCatalogMissingSchemaOperationsStay501(t *testing.T) {
	s := &Server{}
	ctx := context.Background()

	if _, err := s.ListAdminMailTemplates(ctx, gen.ListAdminMailTemplatesRequestObject{}); !errors.Is(err, ErrNotImplemented) {
		t.Fatal("mail_templates 表不存在，ListAdminMailTemplates 必须保持 501")
	}
	if _, err := s.UpdateAdminMailTemplate(ctx, gen.UpdateAdminMailTemplateRequestObject{}); !errors.Is(err, ErrNotImplemented) {
		t.Fatal("mail_templates 表不存在，UpdateAdminMailTemplate 必须保持 501")
	}
	if _, err := s.ListAdminDomains(ctx, gen.ListAdminDomainsRequestObject{}); !errors.Is(err, ErrNotImplemented) {
		t.Fatal("domains 表不存在（ADR 0010/0011 未批准），ListAdminDomains 必须保持 501")
	}
	if _, err := s.CreateAdminDomain(ctx, gen.CreateAdminDomainRequestObject{}); !errors.Is(err, ErrNotImplemented) {
		t.Fatal("domains 表不存在（ADR 0010/0011 未批准），CreateAdminDomain 必须保持 501")
	}
	if _, err := s.DeleteAdminDomain(ctx, gen.DeleteAdminDomainRequestObject{}); !errors.Is(err, ErrNotImplemented) {
		t.Fatal("domains 表不存在（ADR 0010/0011 未批准），DeleteAdminDomain 必须保持 501")
	}
}

// ============================================================
// 四层强制的共用部件
// ============================================================

func TestCatalogCheckReason(t *testing.T) {
	t.Run("少于 8 个字符 → 422（L2）", func(t *testing.T) {
		if err := catalogCheckReason("太短了"); err == nil {
			t.Fatal("3 个字的原因必须被拒")
		}
	})
	// 🔴 按 rune 不按字节：一条 6 个汉字的原因是 18 字节，
	//    按字节算会轻松通过 8 的门槛，而契约说的是 8 个**字符**。
	t.Run("🔴 按字符数不按字节数：6 个汉字（18 字节）仍然不够", func(t *testing.T) {
		r := "网关回调丢失"
		if len(r) < adminReasonMinRunes {
			t.Fatal("前提不成立：这个串的字节数本该 ≥ 8")
		}
		if err := catalogCheckReason(r); err == nil {
			t.Fatal("6 个汉字必须被拒 —— 否则 L2 的门槛在中文下形同虚设")
		}
	})
	t.Run("只有空白 → 422（trim 之后是空串）", func(t *testing.T) {
		if err := catalogCheckReason("            "); err == nil {
			t.Fatal("空白原因必须被拒，否则审计里会出现什么都没解释的记录")
		}
	})
	t.Run("超长 → 422（audit_logs 是 append-only 永不删除的表）", func(t *testing.T) {
		if err := catalogCheckReason(strings.Repeat("字", catalogReasonMaxRunes+1)); err == nil {
			t.Fatal("超长原因必须被拒")
		}
	})
	t.Run("正常路径", func(t *testing.T) {
		if err := catalogCheckReason("链上到账但网关回调丢失，人工补单"); err != nil {
			t.Fatalf("合法原因不该被拒：%v", err)
		}
	})
}

func TestCatalogRoleGates(t *testing.T) {
	// L4 的替代物：库里没有 admin.*.write 那五个列，只能由角色推。
	if !catalogRoleCanWrite(middleware.RoleOwner) || !catalogRoleCanWrite(middleware.RoleAdmin) {
		t.Fatal("owner / admin 必须能做配置类写操作")
	}
	if catalogRoleCanWrite(middleware.RoleSupport) {
		t.Fatal("support（客服）不能改套餐 / 优惠码 / 配置 —— 没有这道闸，一个客服账号可以改全站价格")
	}
	// 工单是客服的本职：把他挡在外面的现实后果是「所有人都被设成 admin」，
	// 那样上面那道闸也一起没了。
	if !catalogRoleCanWriteTicket(middleware.RoleSupport) {
		t.Fatal("support 必须能写工单")
	}
	if catalogRoleCanWriteTicket("") {
		t.Fatal("未知角色一律拒绝")
	}
}

// 🔴 审计过滤必须是**包含匹配**，且元字符必须先转义。
func TestAuditActionFilter(t *testing.T) {
	t.Run("🔴 包含匹配：库里 action 带 D 编号前缀，等值匹配一条都查不到且不报错", func(t *testing.T) {
		got := auditActionFilter(ptrOf("order.mark_paid"))
		if got == nil {
			t.Fatal("传了 action 就该有过滤")
		}
		if !strings.HasPrefix(*got, "%") || !strings.HasSuffix(*got, "%") {
			t.Fatalf("必须是 %%…%% 的包含模式，实际 %q", *got)
		}
		// `_` 必须被转义 —— 不转义的话它匹配任意单字符，会安静地多返回一批记录。
		if !strings.Contains(*got, `\_`) {
			t.Fatalf("下划线必须转义（它是 LIKE 的元字符），实际 %q", *got)
		}
	})
	t.Run("🔴 单个 %% 不转义 = 匹配全部，过滤器形同虚设", func(t *testing.T) {
		got := auditActionFilter(ptrOf("%"))
		if got == nil || *got != `%\%%` {
			t.Fatalf("百分号必须转义，实际 %v", got)
		}
	})
	t.Run("反斜杠先转义（否则它会吃掉后面那个字符）", func(t *testing.T) {
		got := auditActionFilter(ptrOf(`a\b`))
		if got == nil || *got != `%a\\b%` {
			t.Fatalf("反斜杠必须先于其余元字符被转义，实际 %v", got)
		}
	})
	t.Run("空 / 只有空白 → 不过滤（而不是过滤成空串）", func(t *testing.T) {
		if auditActionFilter(nil) != nil {
			t.Fatal("没传就不该有过滤")
		}
		if auditActionFilter(ptrOf("   ")) != nil {
			t.Fatal("只有空白等于没传 —— 过滤成 %%  %% 会返回 0 行且不报错")
		}
	})
}

// 🔴 统计的时区换算：错开一天的日报看起来完全正常，只是每天的数字都是隔壁那天的。
func TestCatalogShanghaiConversion(t *testing.T) {
	t.Run("🔴 UTC 的 8/29 20:00 属于上海的 8/30", func(t *testing.T) {
		utc := time.Date(2026, 8, 29, 20, 0, 0, 0, time.UTC)
		d := catalogStatDate(utc)
		if !d.Valid {
			t.Fatal("必须有效")
		}
		y, m, day := d.Time.Date()
		if y != 2026 || m != time.August || day != 30 {
			t.Fatalf("按上海切天应当是 2026-08-30，实际 %04d-%02d-%02d", y, m, day)
		}
	})
	t.Run("record_at 是上海当天 00:00 对应的 UTC 时刻（= 前一天 16:00Z）", func(t *testing.T) {
		d := pgtype.Date{Time: time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC), Valid: true}
		got := catalogRecordAt(d).UTC()
		want := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Fatalf("record_at 应当是 %s，实际 %s —— 直接当成 UTC 00:00 发出去会让每个点晚 8 小时", want, got)
		}
	})
	t.Run("NULL date → 零值（不是 1970）", func(t *testing.T) {
		if !catalogRecordAt(pgtype.Date{}).IsZero() {
			t.Fatal("无效日期应当返回零值")
		}
	})
}

func TestCatalogJSONObject(t *testing.T) {
	t.Run("对象原样解出来", func(t *testing.T) {
		m := catalogJSONObject([]byte(`{"a":1}`))
		if m == nil || (*m)["a"] != float64(1) {
			t.Fatalf("对象应当原样解出，实际 %v", m)
		}
	})
	// 审计快照理论上永远是对象，但 audit.Entry.Before/After 收的是 any ——
	// 某天有人传了切片，那一列就是数组。硬解会失败，而丢弃它是毁证据。
	t.Run("非对象（数组）包成 {\"value\": …} 而不是丢弃", func(t *testing.T) {
		m := catalogJSONObject([]byte(`[1,2]`))
		if m == nil {
			t.Fatal("不能丢弃 —— 审计记录的价值在于它当时长什么样")
		}
		if _, ok := (*m)["value"]; !ok {
			t.Fatalf("应当包进 value 键，实际 %v", *m)
		}
	})
	t.Run("空字节 → nil（SQL NULL 与 JSON null 是两回事）", func(t *testing.T) {
		if catalogJSONObject(nil) != nil {
			t.Fatal("NULL 应当映射成 nil")
		}
	})
}

// ============================================================
// 套餐（D8）
// ============================================================

func TestPlanKindFromContract(t *testing.T) {
	if k, _ := planKindFromContract(gen.Period); k != planKindCycle {
		t.Fatalf("period 必须映射成 cycle，实际 %q", k)
	}
	if k, _ := planKindFromContract(gen.TrafficPack); k != planKindPack {
		t.Fatalf("traffic_pack 必须映射成 pack，实际 %q", k)
	}
	if _, err := planKindFromContract("period_x"); err == nil {
		t.Fatal("未知类型必须 422 —— 静默落到 cycle 正是 0016 不给 DEFAULT 要防的那件事")
	}
}

func TestParsePlanPrices(t *testing.T) {
	t.Run("正常路径：只有非零周期出现在结果里", func(t *testing.T) {
		p, err := parsePlanPrices([]gen.PlanPrice{
			{Period: gen.PlanPricePeriodMonthly, Amount: 6000},
			{Period: gen.PlanPricePeriodYearly, Amount: 61200},
		}, planKindCycle)
		if err != nil {
			t.Fatal(err)
		}
		if p.monthly == nil || *p.monthly != 6000 || p.yearly == nil || *p.yearly != 61200 {
			t.Fatal("月付与年付必须落到对应列")
		}
		if p.quarterly != nil || p.halfYearly != nil || p.onetime != nil {
			t.Fatal("没给的周期必须是 NULL（= 该周期不售），不是 0（= 免费）")
		}
	})
	// 🔴 plans_cycle_needs_monthly（0016）：退款公式按月单价折算，
	//    没有月价的周期套餐退不了款。
	t.Run("🔴 周期套餐没有月付价 → 422（退款公式会除到一个不存在的数上）", func(t *testing.T) {
		_, err := parsePlanPrices([]gen.PlanPrice{{Period: gen.PlanPricePeriodYearly, Amount: 61200}}, planKindCycle)
		if err == nil {
			t.Fatal("必须被拒")
		}
	})
	t.Run("加油包只给一次性价即可（它没有月价的概念）", func(t *testing.T) {
		if _, err := parsePlanPrices([]gen.PlanPrice{{Period: gen.PlanPricePeriodOnetime, Amount: 1200}}, planKindPack); err != nil {
			t.Fatalf("加油包不该被要求月价：%v", err)
		}
	})
	// 🔴 契约的枚举含 two_yearly / three_yearly，而 plans 没有这两列。
	//    静默丢弃的现象是「我明明设了两年价，保存后没了」。
	t.Run("🔴 契约里有但库里没有的周期 → 422，不静默丢弃", func(t *testing.T) {
		for _, p := range []gen.PlanPricePeriod{gen.PlanPricePeriodTwoYearly, gen.PlanPricePeriodThreeYearly} {
			_, err := parsePlanPrices([]gen.PlanPrice{
				{Period: gen.PlanPricePeriodMonthly, Amount: 100},
				{Period: p, Amount: 100},
			}, planKindCycle)
			if err == nil {
				t.Fatalf("%s 必须被明确拒绝", p)
			}
		}
	})
	t.Run("同一周期出现两次 → 422（否则后一条静默覆盖前一条）", func(t *testing.T) {
		_, err := parsePlanPrices([]gen.PlanPrice{
			{Period: gen.PlanPricePeriodMonthly, Amount: 100},
			{Period: gen.PlanPricePeriodMonthly, Amount: 200},
		}, planKindCycle)
		if err == nil {
			t.Fatal("重复周期必须被拒")
		}
	})
	t.Run("负价 / 空数组 → 422", func(t *testing.T) {
		if _, err := parsePlanPrices([]gen.PlanPrice{{Period: gen.PlanPricePeriodMonthly, Amount: -1}}, planKindCycle); err == nil {
			t.Fatal("负价必须被拒")
		}
		if _, err := parsePlanPrices(nil, planKindPack); err == nil {
			t.Fatal("没有任何价格的套餐是买不了的套餐")
		}
	})
}

// 🔴 契约说「第一阶段全部 0（不限）」，库里 CHECK (speed_limit_mbps > 0)，不限速是 NULL。
func TestPlanSpeedLimitZeroBecomesNull(t *testing.T) {
	if planSpeedLimit(ptrOf(int32(0))) != nil {
		t.Fatal("0 必须翻成 NULL，否则每次保存套餐都是一个 23514")
	}
	if planSpeedLimit(nil) != nil {
		t.Fatal("没给也是 NULL")
	}
	if v := planSpeedLimit(ptrOf(int32(100))); v == nil || *v != 100 {
		t.Fatal("正数原样传")
	}
}

func TestPlanDeviceLimitZeroBecomesNull(t *testing.T) {
	if planDeviceLimit(0) != nil {
		t.Fatal("0 = 不限设备 → NULL（契约与 catalog.go 的 planView 是同一个约定）")
	}
	if v := planDeviceLimit(5); v == nil || *v != 5 {
		t.Fatal("正数原样传")
	}
}

func TestPlanResetMethodFor(t *testing.T) {
	// 🔴 会重置的加油包 = 每月白送一次流量。
	if planResetMethodFor(planKindPack) != dbgen.ResetMethodNever {
		t.Fatal("加油包必须永不重置")
	}
	if planResetMethodFor(planKindCycle) != dbgen.ResetMethodMonthlyOnOrderDay {
		t.Fatal("周期套餐按下单日重置")
	}
}

type fakePlanRegistry struct {
	groupID int64
	err     error
}

func (f *fakePlanRegistry) GetRegistrationGroupID(context.Context) (int64, error) {
	return f.groupID, f.err
}

func validPlanUpsert() gen.PlanUpsert {
	return gen.PlanUpsert{
		Name:                "标准",
		Type:                gen.Period,
		Reason:              "按定价修订 A3 调整标准档月付价",
		TransferEnableBytes: 100 << 30,
		DeviceLimit:         5,
		Prices:              []gen.PlanPrice{{Period: gen.PlanPricePeriodMonthly, Amount: 6000}},
	}
}

func TestCreateAdminPlan(t *testing.T) {
	// 🔴 本组最重要的一条：kind 必须被显式写进去。
	//    漏了它 = 后台建出来的加油包被静默写成周期套餐，
	//    然后 POST /orders 把它推导成 upgrade、凭空触发一次折抵。
	t.Run("🔴 kind 必须显式写入（加油包不能被静默写成周期套餐）", func(t *testing.T) {
		tx := &fakeCatalogTx{createPlanRow: dbgen.Plan{ID: 11}}
		run := &fakeAuditRun{tx: tx}
		body := validPlanUpsert()
		body.Type = gen.TrafficPack
		body.Prices = []gen.PlanPrice{{Period: gen.PlanPricePeriodOnetime, Amount: 1200}}

		p, err := createAdminPlan(context.Background(), &fakePlanRegistry{groupID: 3}, run.runner(), testActor(), body)
		if err != nil {
			t.Fatalf("不该失败：%v", err)
		}
		if tx.createPlanArg == nil {
			t.Fatal("没有调用 CreatePlan")
		}
		if tx.createPlanArg.Kind != planKindPack {
			t.Fatalf("kind 必须是 %q，实际 %q —— 这正是 0016 不给 DEFAULT 要防的静默错误分类",
				planKindPack, tx.createPlanArg.Kind)
		}
		if p.Type != gen.PlanTypeTrafficPack {
			t.Fatalf("响应的 type 应当是 traffic_pack，实际 %q", p.Type)
		}
		if tx.createPlanArg.ResetTrafficMethod != dbgen.ResetMethodNever {
			t.Fatal("加油包必须 never 重置")
		}
	})

	t.Run("code 与 group_id 由服务端补齐（契约里没有这两个字段）", func(t *testing.T) {
		tx := &fakeCatalogTx{createPlanRow: dbgen.Plan{ID: 12}}
		run := &fakeAuditRun{tx: tx}
		if _, err := createAdminPlan(context.Background(), &fakePlanRegistry{groupID: 42}, run.runner(), testActor(), validPlanUpsert()); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(tx.createPlanArg.Code, planCodePrefix) {
			t.Fatalf("code 必须由服务端生成且带固定前缀，实际 %q", tx.createPlanArg.Code)
		}
		if tx.createPlanArg.GroupID != 42 {
			t.Fatalf("group_id 必须取默认注册分组，实际 %d", tx.createPlanArg.GroupID)
		}
		if !tx.createPlanArg.Sellable || !tx.createPlanArg.Renewable {
			t.Fatal("契约里没有这两个字段，默认必须是可卖可续 —— 建出来就不能卖的套餐没有意义")
		}
	})

	// 「参数没收齐时不许提交」：L2。
	t.Run("🔴 reason < 8 字符 → 422，且一条写入都没有发生", func(t *testing.T) {
		tx := &fakeCatalogTx{}
		run := &fakeAuditRun{tx: tx}
		body := validPlanUpsert()
		body.Reason = "改价"
		_, err := createAdminPlan(context.Background(), &fakePlanRegistry{groupID: 1}, run.runner(), testActor(), body)
		oe, ok := asCatalogOpError(err)
		if !ok || oe.kind != catalogErrUnprocessable {
			t.Fatalf("应当是 422，实际 %v", err)
		}
		if run.calls != 0 {
			t.Fatal("L2 没过就不该开事务 —— 参数没收齐时不许提交")
		}
		if tx.createPlanArg != nil {
			t.Fatal("不该有任何写入")
		}
	})

	// 「审计写失败则业务回滚」（api-contract §6.3 第 1 条）。
	t.Run("🔴 审计写失败 → 整个操作失败，不提交", func(t *testing.T) {
		tx := &fakeCatalogTx{createPlanRow: dbgen.Plan{ID: 13}}
		run := &fakeAuditRun{tx: tx, auditErr: errors.New("写审计日志失败")}
		_, err := createAdminPlan(context.Background(), &fakePlanRegistry{groupID: 1}, run.runner(), testActor(), validPlanUpsert())
		if err == nil {
			t.Fatal("审计写失败必须让整个操作失败 —— 否则「业务成功、审计缺失」变成静默的可能")
		}
		if run.committed {
			t.Fatal("不能提交")
		}
	})

	t.Run("撞 code 唯一索引 → 换一个码重试（不是 500）", func(t *testing.T) {
		tx := &fakeCatalogTx{
			createPlanRow:  dbgen.Plan{ID: 14},
			createPlanErrs: []error{pgErr("23505")},
		}
		run := &fakeAuditRun{tx: tx}
		if _, err := createAdminPlan(context.Background(), &fakePlanRegistry{groupID: 1}, run.runner(), testActor(), validPlanUpsert()); err != nil {
			t.Fatalf("撞码应当重试成功：%v", err)
		}
		if run.calls != 2 {
			t.Fatalf("应当重试一次（两次事务），实际 %d 次", run.calls)
		}
	})

	t.Run("数据库 CHECK 拒绝 → 422 而不是 500（数据库是对的，请求是错的）", func(t *testing.T) {
		tx := &fakeCatalogTx{createPlanErrs: []error{pgErr("23514")}}
		run := &fakeAuditRun{tx: tx}
		_, err := createAdminPlan(context.Background(), &fakePlanRegistry{groupID: 1}, run.runner(), testActor(), validPlanUpsert())
		oe, ok := asCatalogOpError(err)
		if !ok || oe.kind != catalogErrUnprocessable {
			t.Fatalf("23514 必须翻成 422，实际 %v", err)
		}
	})

	t.Run("审计条目：action 带 D 编号，after 里带「只影响新订单」那句话", func(t *testing.T) {
		tx := &fakeCatalogTx{createPlanRow: dbgen.Plan{ID: 15}}
		run := &fakeAuditRun{tx: tx}
		if _, err := createAdminPlan(context.Background(), &fakePlanRegistry{groupID: 1}, run.runner(), testActor(), validPlanUpsert()); err != nil {
			t.Fatal(err)
		}
		e := run.lastEntry(t)
		if e.Action != "D8.plan.create" {
			t.Fatalf("action 必须带 D 编号（库里的形态），实际 %q", e.Action)
		}
		if e.TargetType != "plan" || e.TargetID != "15" {
			t.Fatalf("target 不对：%s/%s", e.TargetType, e.TargetID)
		}
		if e.Before != nil {
			t.Fatal("创建操作没有 before")
		}
		snap, ok := e.After.(planSnapshot)
		if !ok {
			t.Fatalf("after 应当是 planSnapshot，实际 %T", e.After)
		}
		if snap.PricingScopeNote != planPricingScopeNotice {
			t.Fatal("🔴 D8 的审计必须带「改套餐只影响新订单」那句话 —— 契约里没有 confirmation 字段，" +
				"这是唯一能记录「他当时被告知过什么」的地方")
		}
		if e.Reason == "" {
			t.Fatal("reason 必须进审计")
		}
	})
}

func TestUpdateAdminPlan(t *testing.T) {
	base := func() *fakeCatalogTx {
		return &fakeCatalogTx{
			getPlanRow: dbgen.AdminGetPlanForUpdateRow{ID: 7, Kind: planKindCycle, SortOrder: 3},
			updatePlanRow: dbgen.AdminUpdatePlanRow{
				ID: 7, Code: "plan_abc", Name: "标准", Kind: planKindCycle,
				BeforeName: "旧名", BeforeKind: planKindCycle,
				BeforePriceMonthly: ptrOf(int64(5000)), PriceMonthly: ptrOf(int64(6000)),
			},
		}
	}

	t.Run("正常路径：before 取查询自己给的 before_*（不是先读的那一份）", func(t *testing.T) {
		tx := base()
		run := &fakeAuditRun{tx: tx}
		if _, err := updateAdminPlan(context.Background(), run.runner(), testActor(), 7, validPlanUpsert()); err != nil {
			t.Fatal(err)
		}
		e := run.lastEntry(t)
		snap, ok := e.Before.(planSnapshot)
		if !ok {
			t.Fatalf("before 应当是 planSnapshot，实际 %T", e.Before)
		}
		if snap.Name != "旧名" || snap.PriceMonthly == nil || *snap.PriceMonthly != 5000 {
			t.Fatal("🔴 before 必须来自 UPDATE 语句里的 prev 侧 —— " +
				"用先读的那一份在并发下会记下一个从未紧接着 after 出现过的快照")
		}
		after := e.After.(planSnapshot)
		if after.PriceMonthly == nil || *after.PriceMonthly != 6000 {
			t.Fatal("after 必须是改后值")
		}
	})

	t.Run("套餐不存在 → 404", func(t *testing.T) {
		tx := base()
		tx.getPlanErr = pgx.ErrNoRows
		run := &fakeAuditRun{tx: tx}
		_, err := updateAdminPlan(context.Background(), run.runner(), testActor(), 7, validPlanUpsert())
		oe, ok := asCatalogOpError(err)
		if !ok || oe.kind != catalogErrNotFound {
			t.Fatalf("应当是 404，实际 %v", err)
		}
	})

	// 🔴 改 kind 会让同一个套餐的历史订单分成互相矛盾的两半，且三条路径都不报错。
	t.Run("🔴 已有订单时改 type → 422（订单的类型是下单时定死存进订单行的）", func(t *testing.T) {
		tx := base()
		tx.getPlanRow.OrderCount = 12
		run := &fakeAuditRun{tx: tx}
		body := validPlanUpsert()
		body.Type = gen.TrafficPack
		body.Prices = []gen.PlanPrice{{Period: gen.PlanPricePeriodOnetime, Amount: 1200}}
		_, err := updateAdminPlan(context.Background(), run.runner(), testActor(), 7, body)
		oe, ok := asCatalogOpError(err)
		if !ok || oe.kind != catalogErrUnprocessable {
			t.Fatalf("应当是 422，实际 %v", err)
		}
		if tx.updatePlanArg != nil {
			t.Fatal("不该发生写入")
		}
	})

	t.Run("没有订单时允许改 type（那是一次修正，不是数据事故）", func(t *testing.T) {
		tx := base()
		tx.getPlanRow.OrderCount = 0
		tx.updatePlanRow.Kind = planKindPack
		run := &fakeAuditRun{tx: tx}
		body := validPlanUpsert()
		body.Type = gen.TrafficPack
		body.Prices = []gen.PlanPrice{{Period: gen.PlanPricePeriodOnetime, Amount: 1200}}
		if _, err := updateAdminPlan(context.Background(), run.runner(), testActor(), 7, body); err != nil {
			t.Fatalf("不该被拒：%v", err)
		}
		if tx.updatePlanArg.Kind != planKindPack {
			t.Fatal("kind 必须传下去")
		}
	})

	t.Run("🔴 reason < 8 字符 → 422，且一条写入都没有发生", func(t *testing.T) {
		tx := base()
		run := &fakeAuditRun{tx: tx}
		body := validPlanUpsert()
		body.Reason = "改一下"
		if _, err := updateAdminPlan(context.Background(), run.runner(), testActor(), 7, body); err == nil {
			t.Fatal("必须被拒")
		}
		if run.calls != 0 || tx.updatePlanArg != nil {
			t.Fatal("参数没收齐时不许提交")
		}
	})

	t.Run("🔴 审计写失败 → 整个操作失败，不提交", func(t *testing.T) {
		tx := base()
		run := &fakeAuditRun{tx: tx, auditErr: errors.New("boom")}
		if _, err := updateAdminPlan(context.Background(), run.runner(), testActor(), 7, validPlanUpsert()); err == nil {
			t.Fatal("必须失败")
		}
		if run.committed {
			t.Fatal("不能提交")
		}
	})

	t.Run("sort 未传时保持现值（不是清成 0）", func(t *testing.T) {
		tx := base()
		run := &fakeAuditRun{tx: tx}
		if _, err := updateAdminPlan(context.Background(), run.runner(), testActor(), 7, validPlanUpsert()); err != nil {
			t.Fatal(err)
		}
		if tx.updatePlanArg.SortOrder != 3 {
			t.Fatalf("未传的 sort 应当保持现值 3，实际 %d", tx.updatePlanArg.SortOrder)
		}
	})
}

func TestDeleteAdminPlan(t *testing.T) {
	base := func() *fakeCatalogTx {
		return &fakeCatalogTx{
			getPlanRow: dbgen.AdminGetPlanForUpdateRow{ID: 9, Name: "标准", Kind: planKindCycle},
			archivePlanRow: dbgen.AdminArchivePlanRow{
				ID: 9, Code: "plan_x", Name: "标准", Kind: planKindCycle,
				BeforeName: "标准", BeforeSellable: true, BeforeVisible: true,
				Sellable: false, Visible: false,
				ArchivedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
			},
		}
	}

	t.Run("正常路径：下架而不是删除，before/after 都进审计", func(t *testing.T) {
		tx := base()
		run := &fakeAuditRun{tx: tx}
		if err := deleteAdminPlan(context.Background(), run.runner(), testActor(), 9); err != nil {
			t.Fatal(err)
		}
		e := run.lastEntry(t)
		if e.Action != "D8.plan.archive" {
			t.Fatalf("action 应当说明这是下架不是删除，实际 %q", e.Action)
		}
		before := e.Before.(planSnapshot)
		after := e.After.(planSnapshot)
		if before.Sellable == nil || !*before.Sellable || after.Sellable == nil || *after.Sellable {
			t.Fatal("sellable 的前后值必须都在审计里")
		}
		if e.Reason != "" {
			t.Fatal("契约给 DELETE 没有请求体，reason 只能是空 —— 编一个是撒谎")
		}
	})

	t.Run("套餐不存在 → 404", func(t *testing.T) {
		tx := base()
		tx.getPlanErr = pgx.ErrNoRows
		run := &fakeAuditRun{tx: tx}
		err := deleteAdminPlan(context.Background(), run.runner(), testActor(), 9)
		oe, ok := asCatalogOpError(err)
		if !ok || oe.kind != catalogErrNotFound {
			t.Fatalf("应当是 404，实际 %v", err)
		}
	})

	// 🔴 下架会让还在支付的人走进「套餐不存在」，而他们的钱可能已经在链上了。
	t.Run("🔴 有未结算订单 → 409（他们的钱可能已经在路上）", func(t *testing.T) {
		tx := base()
		tx.getPlanRow.OpenOrderCount = 3
		run := &fakeAuditRun{tx: tx}
		err := deleteAdminPlan(context.Background(), run.runner(), testActor(), 9)
		oe, ok := asCatalogOpError(err)
		if !ok || oe.kind != catalogErrConflict {
			t.Fatalf("应当是 409，实际 %v", err)
		}
	})

	t.Run("有订阅者但没有未结算订单 → 允许（下架的常规语义就是老用户继续用）", func(t *testing.T) {
		tx := base()
		tx.getPlanRow.SubscriberCount = 120
		run := &fakeAuditRun{tx: tx}
		if err := deleteAdminPlan(context.Background(), run.runner(), testActor(), 9); err != nil {
			t.Fatalf("subscriber_count 不该拦住下架：%v", err)
		}
	})

	t.Run("已经下架 → 409（不是幂等 204：那会让人以为刚才那一下起了作用）", func(t *testing.T) {
		tx := base()
		tx.getPlanRow.ArchivedAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
		run := &fakeAuditRun{tx: tx}
		err := deleteAdminPlan(context.Background(), run.runner(), testActor(), 9)
		oe, ok := asCatalogOpError(err)
		if !ok || oe.kind != catalogErrConflict {
			t.Fatalf("应当是 409，实际 %v", err)
		}
	})

	t.Run("🔴 审计写失败 → 不提交", func(t *testing.T) {
		tx := base()
		run := &fakeAuditRun{tx: tx, auditErr: errors.New("boom")}
		if err := deleteAdminPlan(context.Background(), run.runner(), testActor(), 9); err == nil {
			t.Fatal("必须失败")
		}
		if run.committed {
			t.Fatal("不能提交")
		}
	})
}

type fakePlanLister2 struct {
	rows []dbgen.AdminListPlansRow
	err  error
}

func (f *fakePlanLister2) AdminListPlans(context.Context) ([]dbgen.AdminListPlansRow, error) {
	return f.rows, f.err
}

func TestListAdminPlans(t *testing.T) {
	t.Run("已下架的套餐必须列出来（否则误下架的主力套餐再也恢复不了）", func(t *testing.T) {
		f := &fakePlanLister2{rows: []dbgen.AdminListPlansRow{
			{ID: 1, Name: "标准", Kind: planKindCycle, Visible: true, PriceMonthly: ptrOf(int64(6000))},
			{ID: 2, Name: "已下架", Kind: planKindCycle, Visible: false,
				ArchivedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}, PriceMonthly: ptrOf(int64(1))},
		}}
		out, err := listAdminPlans(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 2 {
			t.Fatalf("下架套餐也要列出，实际 %d 条", len(out))
		}
		if out[1].Visible == nil || *out[1].Visible {
			t.Fatal("下架套餐的 visible 必须是 false（契约里没有 archived_at，这是唯一的信号）")
		}
	})
	t.Run("空结果是空切片不是 nil", func(t *testing.T) {
		out, err := listAdminPlans(context.Background(), &fakePlanLister2{})
		if err != nil || out == nil {
			t.Fatal("必须是空切片")
		}
	})
	t.Run("查询失败原样上抛", func(t *testing.T) {
		if _, err := listAdminPlans(context.Background(), &fakePlanLister2{err: errors.New("boom")}); err == nil {
			t.Fatal("必须上报")
		}
	})
}

// ============================================================
// 优惠码
// ============================================================

// 🔴 契约的 percent 是**百分点**（20 = 8 折），库里 percentage 存 **bps**（1000 = 10%）。
// 少乘 100 → 用户实际只打 0.2% 折；多乘 → 白送。两个方向都不报错。
func TestCouponValueDimension(t *testing.T) {
	t.Run("🔴 percent 20 个百分点 → 库里 2000 bps", func(t *testing.T) {
		v, err := couponValueToDB(gen.CouponUpsertTypePercent, 20)
		if err != nil {
			t.Fatal(err)
		}
		if v != 2000 {
			t.Fatalf("20 个百分点必须写成 2000 bps，实际 %d —— %d 会让用户只打 %.2f%% 折",
				v, v, float64(v)/100)
		}
	})
	t.Run("🔴 读回来要除以 100", func(t *testing.T) {
		v, exact := couponValueFromDB(couponTypePercentDB, 2000)
		if v != 20 || !exact {
			t.Fatalf("2000 bps 应当读成 20 个百分点，实际 %d", v)
		}
	})
	t.Run("fixed 是分，两个方向都原样", func(t *testing.T) {
		v, err := couponValueToDB(gen.CouponUpsertTypeFixed, 1500)
		if err != nil || v != 1500 {
			t.Fatalf("固定额必须原样（单位分），实际 %d", v)
		}
		back, exact := couponValueFromDB(couponTypeFixedDB, 1500)
		if back != 1500 || !exact {
			t.Fatal("读回来也原样")
		}
	})
	t.Run("🔴 bps 不是 100 的整数倍时报告截断（10.5% 会显示成 10%）", func(t *testing.T) {
		v, exact := couponValueFromDB(couponTypePercentDB, 1050)
		if v != 10 || exact {
			t.Fatalf("1050 bps 应当截断成 10 并报告不精确，实际 v=%d exact=%v", v, exact)
		}
	})
	t.Run("value <= 0 → 422（value > 0 是 CHECK）", func(t *testing.T) {
		if _, err := couponValueToDB(gen.CouponUpsertTypePercent, 0); err == nil {
			t.Fatal("0 必须被拒 —— 想停用请用 enabled=false")
		}
	})
	t.Run("percent > 100 → 422（否则折扣超过原价，现象是下单时 500）", func(t *testing.T) {
		if _, err := couponValueToDB(gen.CouponUpsertTypePercent, 101); err == nil {
			t.Fatal("超过 100 个百分点必须被拒")
		}
	})
	t.Run("type 映射两个方向", func(t *testing.T) {
		if v, _ := couponTypeToDB(gen.CouponUpsertTypeFixed); v != couponTypeFixedDB {
			t.Fatal("fixed → fixed_amount")
		}
		if v, _ := couponTypeToDB(gen.CouponUpsertTypePercent); v != couponTypePercentDB {
			t.Fatal("percent → percentage")
		}
		if _, ok := couponTypeFromDB("weird"); ok {
			t.Fatal("未知类型必须被报告，不能静默当成 fixed")
		}
	})
}

// 🔴 库里没有 enabled 列，而 visible 不是它：visible=false 的优惠码照样能兑换。
func TestCouponEndsAtForWrite(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	future := now.Add(72 * time.Hour)
	past := now.Add(-72 * time.Hour)

	t.Run("🔴 enabled=false → ends_at = now（唯一真能停掉它的机制）", func(t *testing.T) {
		got := couponEndsAtForWrite(ptrOf(false), &future, nil, now)
		if !got.Valid || !got.Time.Equal(now) {
			t.Fatalf("必须把结束时间设成现在（即使 body 给了一个未来时间），实际 %v", got)
		}
	})
	t.Run("🔴 enabled=true 且现有 ends_at 还没到 → 不要动它（盲目清会让真到期的活动复活）", func(t *testing.T) {
		cur := tstz(future)
		got := couponEndsAtForWrite(ptrOf(true), nil, &cur, now)
		if !got.Valid || !got.Time.Equal(future) {
			t.Fatalf("未过期的 ends_at 必须保持，实际 %v", got)
		}
	})
	t.Run("enabled=true 且现有 ends_at 已过期 → 清空（= 重新启用）", func(t *testing.T) {
		cur := tstz(past)
		got := couponEndsAtForWrite(ptrOf(true), nil, &cur, now)
		if got.Valid {
			t.Fatal("已过期时才允许清空")
		}
	})
	t.Run("enabled 未传 → 完全按 body 的 ended_at（「没提」不是「要改」）", func(t *testing.T) {
		cur := tstz(future)
		got := couponEndsAtForWrite(nil, nil, &cur, now)
		if got.Valid {
			t.Fatal("没提 enabled 且没给 ended_at → 按 body 走（清空）")
		}
		got2 := couponEndsAtForWrite(nil, &past, &cur, now)
		if !got2.Valid || !got2.Time.Equal(past) {
			t.Fatal("给了 ended_at 就用它")
		}
	})
}

func TestCouponEnabledNow(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	if couponEnabledNow(tstz(now.Add(time.Hour)), pgtype.Timestamptz{}, nil, 0, now) {
		t.Fatal("还没开始的码不可用")
	}
	if couponEnabledNow(pgtype.Timestamptz{}, tstz(now), nil, 0, now) {
		t.Fatal("ends_at == now 就已经结束（SQL 里是 ends_at > now()）")
	}
	if couponEnabledNow(pgtype.Timestamptz{}, pgtype.Timestamptz{}, ptrOf(int32(5)), 5, now) {
		t.Fatal("用尽的码不可用")
	}
	if !couponEnabledNow(pgtype.Timestamptz{}, pgtype.Timestamptz{}, nil, 100, now) {
		t.Fatal("没有任何限制的码永远可用")
	}
}

func validCouponUpsert() gen.CouponUpsert {
	return gen.CouponUpsert{
		Code:   "spring20",
		Type:   gen.CouponUpsertTypePercent,
		Value:  20,
		Reason: "春季活动，八折券，运营已批",
	}
}

func TestCreateAdminCoupon(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

	t.Run("正常路径：percent 20 写成 2000 bps，visible 恒为 true", func(t *testing.T) {
		tx := &fakeCatalogTx{createCouponRow: dbgen.Coupon{ID: 5}}
		run := &fakeAuditRun{tx: tx}
		var warns []string
		c, err := createAdminCoupon(context.Background(), run.runner(), testActor(), validCouponUpsert(), now, collectWarn(&warns))
		if err != nil {
			t.Fatal(err)
		}
		if tx.createCouponArg.Value != 2000 {
			t.Fatalf("必须写 bps，实际 %d", tx.createCouponArg.Value)
		}
		if !tx.createCouponArg.Visible {
			t.Fatal("🔴 visible 不是 enabled：契约的 CouponUpsert 里根本没有 visible 字段，" +
				"把「禁用」接到它上面会得到一个看起来禁用了、实际还在打折的码")
		}
		if c.Value != 20 {
			t.Fatalf("响应里必须换回百分点，实际 %d", c.Value)
		}
		if c.Type != gen.CouponTypePercent {
			t.Fatal("type 必须映射回契约枚举")
		}
	})

	t.Run("enabled=false 建码 → ends_at 立刻设成现在", func(t *testing.T) {
		tx := &fakeCatalogTx{createCouponRow: dbgen.Coupon{ID: 6}}
		run := &fakeAuditRun{tx: tx}
		body := validCouponUpsert()
		body.Enabled = ptrOf(false)
		var warns []string
		if _, err := createAdminCoupon(context.Background(), run.runner(), testActor(), body, now, collectWarn(&warns)); err != nil {
			t.Fatal(err)
		}
		if !tx.createCouponArg.EndsAt.Valid || !tx.createCouponArg.EndsAt.Time.Equal(now) {
			t.Fatal("停用必须落到 ends_at")
		}
	})

	t.Run("🔴 reason < 8 字符 → 422，一条写入都没有", func(t *testing.T) {
		tx := &fakeCatalogTx{}
		run := &fakeAuditRun{tx: tx}
		body := validCouponUpsert()
		body.Reason = "活动"
		var warns []string
		if _, err := createAdminCoupon(context.Background(), run.runner(), testActor(), body, now, collectWarn(&warns)); err == nil {
			t.Fatal("必须被拒")
		}
		if run.calls != 0 || tx.createCouponArg != nil {
			t.Fatal("参数没收齐时不许提交")
		}
	})

	t.Run("码重复 → 422（契约给这个端点没有 409）", func(t *testing.T) {
		tx := &fakeCatalogTx{createCouponErr: pgErr("23505")}
		run := &fakeAuditRun{tx: tx}
		var warns []string
		_, err := createAdminCoupon(context.Background(), run.runner(), testActor(), validCouponUpsert(), now, collectWarn(&warns))
		oe, ok := asCatalogOpError(err)
		if !ok || oe.kind != catalogErrUnprocessable {
			t.Fatalf("应当是 422，实际 %v", err)
		}
	})

	t.Run("🔴 审计写失败 → 不提交", func(t *testing.T) {
		tx := &fakeCatalogTx{createCouponRow: dbgen.Coupon{ID: 7}}
		run := &fakeAuditRun{tx: tx, auditErr: errors.New("boom")}
		var warns []string
		if _, err := createAdminCoupon(context.Background(), run.runner(), testActor(), validCouponUpsert(), now, collectWarn(&warns)); err == nil {
			t.Fatal("必须失败")
		}
		if run.committed {
			t.Fatal("不能提交")
		}
	})

	t.Run("审计快照带量纲（只写 2000 而不说单位，事后没人分得清 20 元还是 20%）", func(t *testing.T) {
		tx := &fakeCatalogTx{createCouponRow: dbgen.Coupon{ID: 8}}
		run := &fakeAuditRun{tx: tx}
		var warns []string
		if _, err := createAdminCoupon(context.Background(), run.runner(), testActor(), validCouponUpsert(), now, collectWarn(&warns)); err != nil {
			t.Fatal(err)
		}
		snap := run.lastEntry(t).After.(couponSnapshot)
		if snap.ValueUnit == "" || !strings.Contains(snap.ValueUnit, "bps") {
			t.Fatalf("percent 的快照必须标 bps，实际 %q", snap.ValueUnit)
		}
	})

	t.Run("结束时间不晚于开始时间 → 422", func(t *testing.T) {
		tx := &fakeCatalogTx{}
		run := &fakeAuditRun{tx: tx}
		body := validCouponUpsert()
		body.StartedAt = ptrOf(now.Add(time.Hour))
		body.EndedAt = ptrOf(now)
		var warns []string
		if _, err := createAdminCoupon(context.Background(), run.runner(), testActor(), body, now, collectWarn(&warns)); err == nil {
			t.Fatal("必须被拒")
		}
	})
}

func TestUpdateAdminCoupon(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	base := func() *fakeCatalogTx {
		return &fakeCatalogTx{
			getCouponRow: dbgen.AdminGetCouponForUpdateRow{ID: 3, Code: "SPRING20", Type: couponTypePercentDB, Value: 2000, Visible: true},
			updateCouponRow: dbgen.AdminUpdateCouponRow{
				ID: 3, Code: "SPRING20", Type: couponTypePercentDB, Value: 3000,
				BeforeCode: "SPRING20", BeforeType: couponTypePercentDB, BeforeValue: 2000,
				BeforeVisible: true, Visible: true,
			},
		}
	}
	t.Run("正常路径：before/after 都进审计，visible 保持现值", func(t *testing.T) {
		tx := base()
		run := &fakeAuditRun{tx: tx}
		var warns []string
		body := validCouponUpsert()
		body.Value = 30
		if _, err := updateAdminCoupon(context.Background(), run.runner(), testActor(), 3, body, now, collectWarn(&warns)); err != nil {
			t.Fatal(err)
		}
		if tx.updateCouponArg.Value != 3000 {
			t.Fatalf("30 个百分点必须写成 3000 bps，实际 %d", tx.updateCouponArg.Value)
		}
		if !tx.updateCouponArg.Visible {
			t.Fatal("契约里没有 visible 字段，改它就是凭空替管理员做决定")
		}
		e := run.lastEntry(t)
		if e.Before.(couponSnapshot).ValueRaw != 2000 || e.After.(couponSnapshot).ValueRaw != 3000 {
			t.Fatal("前后像必须来自 UPDATE 语句自己给的 before_*/after")
		}
	})
	t.Run("优惠码不存在 → 404", func(t *testing.T) {
		tx := base()
		tx.getCouponErr = pgx.ErrNoRows
		run := &fakeAuditRun{tx: tx}
		var warns []string
		_, err := updateAdminCoupon(context.Background(), run.runner(), testActor(), 3, validCouponUpsert(), now, collectWarn(&warns))
		oe, ok := asCatalogOpError(err)
		if !ok || oe.kind != catalogErrNotFound {
			t.Fatalf("应当是 404，实际 %v", err)
		}
	})
	t.Run("🔴 reason < 8 字符 → 422，不写", func(t *testing.T) {
		tx := base()
		run := &fakeAuditRun{tx: tx}
		body := validCouponUpsert()
		body.Reason = "改"
		var warns []string
		if _, err := updateAdminCoupon(context.Background(), run.runner(), testActor(), 3, body, now, collectWarn(&warns)); err == nil {
			t.Fatal("必须被拒")
		}
		if run.calls != 0 || tx.updateCouponArg != nil {
			t.Fatal("参数没收齐时连事务都不该开")
		}
	})
	t.Run("🔴 审计写失败 → 不提交", func(t *testing.T) {
		tx := base()
		run := &fakeAuditRun{tx: tx, auditErr: errors.New("boom")}
		var warns []string
		if _, err := updateAdminCoupon(context.Background(), run.runner(), testActor(), 3, validCouponUpsert(), now, collectWarn(&warns)); err == nil {
			t.Fatal("必须失败")
		}
		if run.committed {
			t.Fatal("不能提交")
		}
	})
}

func TestDeleteAdminCoupon(t *testing.T) {
	base := func() *fakeCatalogTx {
		return &fakeCatalogTx{
			getCouponRow:    dbgen.AdminGetCouponForUpdateRow{ID: 4, Code: "OLD", Type: couponTypeFixedDB, Value: 1000},
			deleteCouponRow: dbgen.Coupon{ID: 4, Code: "OLD", Type: couponTypeFixedDB, Value: 1000},
		}
	}
	// 🔴 orders.coupon_id 是 ON DELETE SET NULL：删一张用过的码，
	//    历史订单的「为什么少收钱」凭空消失，且无声无息。
	t.Run("🔴 被历史订单引用 → 拒绝（判据是真实引用数不是 used_count）", func(t *testing.T) {
		tx := base()
		tx.getCouponRow.ReferencingOrderCount = 12
		tx.getCouponRow.UsedCount = 0 // 冗余计数可以是 0，判据不看它
		run := &fakeAuditRun{tx: tx}
		err := deleteAdminCoupon(context.Background(), run.runner(), testActor(), 4)
		oe, ok := asCatalogOpError(err)
		if !ok || oe.kind != catalogErrConflict {
			t.Fatalf("应当是 409，实际 %v", err)
		}
	})
	t.Run("正常路径：before 是被删掉的整行，after 为 nil", func(t *testing.T) {
		tx := base()
		run := &fakeAuditRun{tx: tx}
		if err := deleteAdminCoupon(context.Background(), run.runner(), testActor(), 4); err != nil {
			t.Fatal(err)
		}
		e := run.lastEntry(t)
		if e.After != nil {
			t.Fatal("删除操作没有 after")
		}
		if e.Before.(couponSnapshot).Code != "OLD" {
			t.Fatal("🔴 before 是这次删除唯一留下的证据")
		}
	})
	t.Run("不存在 → 404", func(t *testing.T) {
		tx := base()
		tx.getCouponErr = pgx.ErrNoRows
		run := &fakeAuditRun{tx: tx}
		err := deleteAdminCoupon(context.Background(), run.runner(), testActor(), 4)
		oe, ok := asCatalogOpError(err)
		if !ok || oe.kind != catalogErrNotFound {
			t.Fatalf("应当是 404，实际 %v", err)
		}
	})
	t.Run("🔴 审计写失败 → 不提交", func(t *testing.T) {
		tx := base()
		run := &fakeAuditRun{tx: tx, auditErr: errors.New("boom")}
		if err := deleteAdminCoupon(context.Background(), run.runner(), testActor(), 4); err == nil {
			t.Fatal("必须失败")
		}
		if run.committed {
			t.Fatal("不能提交")
		}
	})
}

type fakeCouponLister struct {
	rows    []dbgen.AdminListCouponsPageRow
	total   int64
	arg     dbgen.AdminListCouponsPageParams
	err     error
	counted bool
}

func (f *fakeCouponLister) AdminListCouponsPage(_ context.Context, arg dbgen.AdminListCouponsPageParams) ([]dbgen.AdminListCouponsPageRow, error) {
	f.arg = arg
	return f.rows, f.err
}

func (f *fakeCouponLister) AdminCountCouponsFiltered(context.Context) (int64, error) {
	f.counted = true
	return f.total, nil
}

func TestListAdminCoupons(t *testing.T) {
	now := time.Now()
	mk := func(n int) []dbgen.AdminListCouponsPageRow {
		out := make([]dbgen.AdminListCouponsPageRow, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, dbgen.AdminListCouponsPageRow{
				ID: int64(100 - i), Code: "C", Type: couponTypeFixedDB, Value: 100,
				CreatedAt: tstz(now), Enabled: true,
			})
		}
		return out
	}
	t.Run("多取一行判 has_more，多的那行不下发", func(t *testing.T) {
		f := &fakeCouponLister{rows: mk(3)}
		var warns []string
		data, meta, err := listAdminCoupons(context.Background(), f, gen.Meta{}, gen.ListAdminCouponsParams{Limit: ptrOf(gen.LimitQuery(2))}, now, collectWarn(&warns))
		if err != nil {
			t.Fatal(err)
		}
		if f.arg.PageLimit != 3 {
			t.Fatalf("必须取 limit+1 行，实际 %d", f.arg.PageLimit)
		}
		if len(data) != 2 {
			t.Fatalf("多取的那行不能下发，实际 %d", len(data))
		}
		if meta.HasMore == nil || !*meta.HasMore || meta.NextCursor == nil {
			t.Fatal("has_more 与 next_cursor 都要给")
		}
	})
	t.Run("恰好整除时 has_more = false（不能用「行数 == limit」判）", func(t *testing.T) {
		f := &fakeCouponLister{rows: mk(2)}
		var warns []string
		_, meta, _ := listAdminCoupons(context.Background(), f, gen.Meta{}, gen.ListAdminCouponsParams{Limit: ptrOf(gen.LimitQuery(2))}, now, collectWarn(&warns))
		if meta.HasMore == nil || *meta.HasMore {
			t.Fatal("最后一页不该说还有下一页 —— 用户点进去会看到一页空数据")
		}
	})
	t.Run("坏游标退回第一页并留 WARN（契约给这个端点没有 400）", func(t *testing.T) {
		f := &fakeCouponLister{rows: mk(1)}
		var warns []string
		_, _, err := listAdminCoupons(context.Background(), f, gen.Meta{},
			gen.ListAdminCouponsParams{Cursor: ptrOf(gen.CursorQuery("!!!not-base64!!!"))}, now, collectWarn(&warns))
		if err != nil {
			t.Fatal(err)
		}
		if f.arg.CursorID != nil {
			t.Fatal("坏游标必须被丢弃")
		}
		if len(warns) == 0 {
			t.Fatal("必须留 WARN —— 「翻页按钮好像没反应」这类工单只能靠它回答")
		}
	})
	t.Run("?count=true 才跑 COUNT", func(t *testing.T) {
		f := &fakeCouponLister{rows: mk(1), total: 87}
		var warns []string
		_, meta, _ := listAdminCoupons(context.Background(), f, gen.Meta{}, gen.ListAdminCouponsParams{}, now, collectWarn(&warns))
		if f.counted || meta.Total != nil {
			t.Fatal("没传 count 就不该跑 COUNT")
		}
		f2 := &fakeCouponLister{rows: mk(1), total: 87}
		_, meta2, _ := listAdminCoupons(context.Background(), f2, gen.Meta{}, gen.ListAdminCouponsParams{Count: ptrOf(gen.CountQuery(true))}, now, collectWarn(&warns))
		if meta2.Total == nil || *meta2.Total != 87 {
			t.Fatal("count=true 必须给 total")
		}
	})
}

// ============================================================
// 公告（D12）
// ============================================================

func TestCreateAdminNotice(t *testing.T) {
	body := gen.NoticeUpsert{Title: "域名切换公告", Content: "新域名 example.net"}

	t.Run("正常路径：published_at → starts_at，审计带完整正文", func(t *testing.T) {
		at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
		b := body
		b.PublishedAt = &at
		tx := &fakeCatalogTx{createNoticeRow: dbgen.CreateAdminNoticeRow{ID: 21, Level: "info", Visible: true}}
		run := &fakeAuditRun{tx: tx}
		n, err := createAdminNotice(context.Background(), run.runner(), testActor(), 7, b)
		if err != nil {
			t.Fatal(err)
		}
		if !tx.createNoticeArg.StartsAt.Valid || !tx.createNoticeArg.StartsAt.Time.Equal(at) {
			t.Fatal("published_at 必须落到 starts_at")
		}
		if tx.createNoticeArg.CreatedBy != 7 {
			t.Fatal("created_by 必须是当前管理员")
		}
		if n.Content != "新域名 example.net" {
			t.Fatal("Notice.content ← content_md")
		}
		e := run.lastEntry(t)
		if e.Action != "D12.notice.create" {
			t.Fatalf("action 不对：%q", e.Action)
		}
		if e.After.(noticeSnapshot).ContentMd != "新域名 example.net" {
			t.Fatal("🔴 公告兼域名广播位：事后要查的正是「那天到底广播了哪个域名」，正文必须完整进快照")
		}
		// D12 的 L2 在契约上不存在（NoticeUpsert 没有 reason 字段）。
		if e.Reason != "" {
			t.Fatal("NoticeUpsert 契约里没有 reason —— 编一个是撒谎")
		}
	})

	t.Run("published_at 不传 = 立刻发布（starts_at 为 NULL）", func(t *testing.T) {
		tx := &fakeCatalogTx{createNoticeRow: dbgen.CreateAdminNoticeRow{ID: 22}}
		run := &fakeAuditRun{tx: tx}
		if _, err := createAdminNotice(context.Background(), run.runner(), testActor(), 7, body); err != nil {
			t.Fatal(err)
		}
		if tx.createNoticeArg.StartsAt.Valid {
			t.Fatal("不传就是 NULL")
		}
	})

	t.Run("空标题 / 空正文 / 超长 → 422，且没有写入", func(t *testing.T) {
		for name, b := range map[string]gen.NoticeUpsert{
			"空标题":  {Title: "   ", Content: "x"},
			"空正文":  {Title: "t", Content: "  "},
			"超长正文": {Title: "t", Content: strings.Repeat("字", noticeContentMaxRunes+1)},
		} {
			tx := &fakeCatalogTx{}
			run := &fakeAuditRun{tx: tx}
			if _, err := createAdminNotice(context.Background(), run.runner(), testActor(), 7, b); err == nil {
				t.Fatalf("%s 必须被拒", name)
			}
			if run.calls != 0 {
				t.Fatalf("%s：参数没收齐时不许提交", name)
			}
		}
	})

	t.Run("🔴 审计写失败 → 不提交", func(t *testing.T) {
		tx := &fakeCatalogTx{createNoticeRow: dbgen.CreateAdminNoticeRow{ID: 23}}
		run := &fakeAuditRun{tx: tx, auditErr: errors.New("boom")}
		if _, err := createAdminNotice(context.Background(), run.runner(), testActor(), 7, body); err == nil {
			t.Fatal("必须失败")
		}
		if run.committed {
			t.Fatal("不能提交")
		}
	})
}

func TestUpdateAdminNotice(t *testing.T) {
	base := func() *fakeCatalogTx {
		return &fakeCatalogTx{updateNoticeRow: dbgen.UpdateAdminNoticeRow{
			ID: 31, Title: "新标题", ContentMd: "新正文",
			BeforeTitle: "旧标题", BeforeContentMd: "旧正文",
		}}
	}
	t.Run("title/content 无条件覆写，pinned 未传即不改（契约给的不对称）", func(t *testing.T) {
		tx := base()
		run := &fakeAuditRun{tx: tx}
		if _, err := updateAdminNotice(context.Background(), run.runner(), testActor(), 31,
			gen.NoticeUpsert{Title: "新标题", Content: "新正文"}); err != nil {
			t.Fatal(err)
		}
		if tx.updateNoticeArg.Pinned != nil {
			t.Fatal("pinned 未传时必须是 nil（SQL 里 coalesce 成现值）")
		}
		if tx.updateNoticeArg.Title != "新标题" {
			t.Fatal("title 无条件覆写")
		}
		e := run.lastEntry(t)
		if e.Before.(noticeSnapshot).ContentMd != "旧正文" {
			t.Fatal("前像必须是改前的正文")
		}
	})
	t.Run("公告不存在 → 404", func(t *testing.T) {
		tx := base()
		tx.updateNoticeErr = pgx.ErrNoRows
		run := &fakeAuditRun{tx: tx}
		_, err := updateAdminNotice(context.Background(), run.runner(), testActor(), 31,
			gen.NoticeUpsert{Title: "t", Content: "c"})
		oe, ok := asCatalogOpError(err)
		if !ok || oe.kind != catalogErrNotFound {
			t.Fatalf("应当是 404，实际 %v", err)
		}
	})
	t.Run("🔴 审计写失败 → 不提交", func(t *testing.T) {
		tx := base()
		run := &fakeAuditRun{tx: tx, auditErr: errors.New("boom")}
		if _, err := updateAdminNotice(context.Background(), run.runner(), testActor(), 31,
			gen.NoticeUpsert{Title: "t", Content: "c"}); err == nil {
			t.Fatal("必须失败")
		}
		if run.committed {
			t.Fatal("不能提交")
		}
	})
}

func TestDeleteAdminNotice(t *testing.T) {
	t.Run("正常路径：被删的整行进 before，after 为 nil", func(t *testing.T) {
		tx := &fakeCatalogTx{deleteNoticeRow: dbgen.Notice{ID: 41, Title: "域名广播", ContentMd: "example.net"}}
		run := &fakeAuditRun{tx: tx}
		if err := deleteAdminNoticeTx(context.Background(), run.runner(), testActor(), 41); err != nil {
			t.Fatal(err)
		}
		e := run.lastEntry(t)
		if e.After != nil {
			t.Fatal("删除没有 after")
		}
		if e.Before.(noticeSnapshot).ContentMd != "example.net" {
			t.Fatal("🔴 RETURNING 的整行是这次删除唯一留下的证据")
		}
	})
	t.Run("不存在 → 404", func(t *testing.T) {
		tx := &fakeCatalogTx{deleteNoticeErr: pgx.ErrNoRows}
		run := &fakeAuditRun{tx: tx}
		err := deleteAdminNoticeTx(context.Background(), run.runner(), testActor(), 41)
		oe, ok := asCatalogOpError(err)
		if !ok || oe.kind != catalogErrNotFound {
			t.Fatalf("应当是 404，实际 %v", err)
		}
	})
	t.Run("🔴 审计写失败 → 不提交", func(t *testing.T) {
		tx := &fakeCatalogTx{deleteNoticeRow: dbgen.Notice{ID: 41}}
		run := &fakeAuditRun{tx: tx, auditErr: errors.New("boom")}
		if err := deleteAdminNoticeTx(context.Background(), run.runner(), testActor(), 41); err == nil {
			t.Fatal("必须失败")
		}
		if run.committed {
			t.Fatal("不能提交")
		}
	})
}

type fakeNoticeLister struct {
	rows  []dbgen.ListAdminNoticesPageRow
	arg   dbgen.ListAdminNoticesPageParams
	total int64
}

func (f *fakeNoticeLister) ListAdminNoticesPage(_ context.Context, arg dbgen.ListAdminNoticesPageParams) ([]dbgen.ListAdminNoticesPageRow, error) {
	f.arg = arg
	return f.rows, nil
}
func (f *fakeNoticeLister) CountAdminNotices(context.Context) (int64, error) { return f.total, nil }

func TestListAdminNotices(t *testing.T) {
	created := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	published := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	// 🔴 published_at 是 coalesce(starts_at, created_at) 算出来的，与 ORDER BY 的键不是同一个值。
	//    用它编游标会在有定时发布公告时静默漏行。
	t.Run("🔴 游标用 created_at（排序键）而不是 published_at", func(t *testing.T) {
		f := &fakeNoticeLister{rows: []dbgen.ListAdminNoticesPageRow{
			{ID: 2, CreatedAt: tstz(created), PublishedAt: tstz(published)},
			{ID: 1, CreatedAt: tstz(created), PublishedAt: tstz(published)},
		}}
		var warns []string
		_, meta, err := listAdminNotices(context.Background(), f, gen.Meta{},
			gen.ListAdminNoticesParams{Limit: ptrOf(gen.LimitQuery(1))}, collectWarn(&warns))
		if err != nil {
			t.Fatal(err)
		}
		if meta.NextCursor == nil {
			t.Fatal("应当有下一页游标")
		}
		cur, ok := decodePageCursor(*meta.NextCursor)
		if !ok {
			t.Fatal("游标必须能被自己解开")
		}
		if !cur.At.Equal(created) {
			t.Fatalf("游标的 at 必须是 created_at（%s），实际 %s", created, cur.At)
		}
	})
	t.Run("字段映射：content ← content_md，published_at ← 查询算好的那一列", func(t *testing.T) {
		f := &fakeNoticeLister{rows: []dbgen.ListAdminNoticesPageRow{
			{ID: 1, Title: "t", ContentMd: "正文", Pinned: true, CreatedAt: tstz(created), PublishedAt: tstz(published)},
		}}
		var warns []string
		data, _, _ := listAdminNotices(context.Background(), f, gen.Meta{}, gen.ListAdminNoticesParams{}, collectWarn(&warns))
		if data[0].Content != "正文" || data[0].Pinned == nil || !*data[0].Pinned {
			t.Fatal("字段映射不对")
		}
		if !data[0].PublishedAt.Equal(published) {
			t.Fatal("published_at 必须用查询算好的那一列")
		}
	})
}

// ============================================================
// 邀请码
// ============================================================

func TestCreateAdminInvites(t *testing.T) {
	body := gen.AdminInviteCreateRequest{Count: 3}

	t.Run("正常路径：码由 handler 生成，字符集剔除易混字符", func(t *testing.T) {
		tx := &fakeCatalogTx{}
		run := &fakeAuditRun{tx: tx}
		out, short, err := createAdminInvites(context.Background(), run.runner(), testActor(), body, "01JREQ", "https://app.example.com")
		if err != nil {
			t.Fatal(err)
		}
		if short != 0 || len(out) != 3 {
			t.Fatalf("应当生成 3 个，实际 %d（缺 %d）", len(out), short)
		}
		for _, c := range tx.inviteBatches[0] {
			if strings.ContainsAny(c, "0O1Il") {
				t.Fatalf("码里不能有易混字符：%q", c)
			}
		}
		if out[0].InviteUrl == nil {
			t.Fatal("可用的码要带邀请链接")
		}
	})

	t.Run("count 越界 → 422", func(t *testing.T) {
		for _, n := range []int32{0, adminInviteMaxCount + 1} {
			tx := &fakeCatalogTx{}
			run := &fakeAuditRun{tx: tx}
			if _, _, err := createAdminInvites(context.Background(), run.runner(), testActor(),
				gen.AdminInviteCreateRequest{Count: n}, "r", ""); err == nil {
				t.Fatalf("count=%d 必须被拒", n)
			}
			if run.calls != 0 {
				t.Fatal("参数没收齐时不许提交")
			}
		}
	})

	// 契约的 InviteCode.use_limit 注释说「0 = 不限」，而 max_uses >= 1 是 CHECK。
	// 悄悄改成 1 会让管理员以为造了一批无限次的码。
	t.Run("🔴 use_limit < 1 → 422，不悄悄改成 1", func(t *testing.T) {
		tx := &fakeCatalogTx{}
		run := &fakeAuditRun{tx: tx}
		b := body
		b.UseLimit = ptrOf(int32(0))
		if _, _, err := createAdminInvites(context.Background(), run.runner(), testActor(), b, "r", ""); err == nil {
			t.Fatal("必须被拒并说明本系统没有不限次的邀请码")
		}
	})

	// 🔴 ON CONFLICT DO NOTHING 会让撞码的那些静默消失。
	t.Run("🔴 撞码导致少生成 → 补生成；补不齐则如实上报条数", func(t *testing.T) {
		tx := &fakeCatalogTx{inviteRows: [][]dbgen.InviteCode{
			{{ID: 1, Code: "AAAAAAAA"}}, // 第一轮 3 个只成了 1 个
			{{ID: 2, Code: "BBBBBBBB"}}, // 第二轮 2 个只成了 1 个
			{{ID: 3, Code: "CCCCCCCC"}}, // 第三轮 1 个成了 1 个
		}}
		run := &fakeAuditRun{tx: tx}
		out, short, err := createAdminInvites(context.Background(), run.runner(), testActor(), body, "r", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 3 || short != 0 {
			t.Fatalf("三轮之后应当凑齐 3 个，实际 %d（缺 %d）", len(out), short)
		}
		if len(tx.inviteBatches) != 3 {
			t.Fatalf("应当补生成两轮，实际 %d 轮", len(tx.inviteBatches))
		}
		if len(tx.inviteBatches[1]) != 2 {
			t.Fatalf("第二轮应当只补 2 个，实际 %d", len(tx.inviteBatches[1]))
		}
	})

	t.Run("补不齐时返回实际条数（响应里的 data 就是真码，所以数量天然是对的）", func(t *testing.T) {
		tx := &fakeCatalogTx{inviteRows: [][]dbgen.InviteCode{{}, {}, {}}}
		run := &fakeAuditRun{tx: tx}
		out, short, err := createAdminInvites(context.Background(), run.runner(), testActor(), body, "r", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 0 || short != 3 {
			t.Fatalf("应当如实上报缺 3 个，实际 out=%d short=%d", len(out), short)
		}
	})

	// audit_logs 没有 request_id 列，把它放进 target_id 是把这批码接回访问日志的唯一钥匙。
	t.Run("审计：target_id 用 request_id，码全部进 after 快照", func(t *testing.T) {
		tx := &fakeCatalogTx{}
		run := &fakeAuditRun{tx: tx}
		if _, _, err := createAdminInvites(context.Background(), run.runner(), testActor(), body, "01JREQ", ""); err != nil {
			t.Fatal(err)
		}
		e := run.lastEntry(t)
		if e.TargetID != "01JREQ" {
			t.Fatalf("target_id 应当是 request_id，实际 %q", e.TargetID)
		}
		after := e.After.(map[string]any)
		codes := after["codes"].([]string)
		if len(codes) != 3 {
			t.Fatal("生成了哪些码必须一条不少地进审计")
		}
		if after["owner_user_id"] != nil {
			t.Fatal("这个端点只能造管理员种子码（owner_user_id 恒为 NULL）")
		}
	})

	t.Run("request_id 为空时回退（空 target_id 会让 audit.validate 拒绝整个操作）", func(t *testing.T) {
		tx := &fakeCatalogTx{}
		run := &fakeAuditRun{tx: tx}
		if _, _, err := createAdminInvites(context.Background(), run.runner(), testActor(), body, "", ""); err != nil {
			t.Fatal(err)
		}
		if run.lastEntry(t).TargetID == "" {
			t.Fatal("target_id 不能是空串")
		}
	})

	t.Run("🔴 审计写失败 → 不提交", func(t *testing.T) {
		tx := &fakeCatalogTx{}
		run := &fakeAuditRun{tx: tx, auditErr: errors.New("boom")}
		if _, _, err := createAdminInvites(context.Background(), run.runner(), testActor(), body, "r", ""); err == nil {
			t.Fatal("必须失败")
		}
		if run.committed {
			t.Fatal("不能提交")
		}
	})
}

func TestAdminInviteView(t *testing.T) {
	// status 直接用 SQL 算好的那一列 —— Go 里重算就是两份会漂移的判定。
	r := dbgen.ListAdminInvitesPageRow{ID: 1, Code: "ABC", MaxUses: 5, UsedCount: 5, Status: "exhausted"}
	v := adminInviteView(r, "https://app.example.com")
	if v.Status != gen.InviteCodeStatusExhausted {
		t.Fatal("status 必须用查询给的那一列")
	}
	if v.InviteUrl != nil {
		t.Fatal("用尽的码不该配一个可点的链接 —— 用户会把它发出去，然后对方拿到「邀请码无效」")
	}
	if v.UseLimit == nil || *v.UseLimit != 5 {
		t.Fatal("use_limit 直接给 max_uses（本系统没有不限次的邀请码，契约注释里那个 0 永远不会出现）")
	}
}

// ============================================================
// 佣金调整（D11）
// ============================================================

func TestAdjustAdminCommission(t *testing.T) {
	okRow := dbgen.AdjustAdminCommissionAmountRow{
		ID: 5, OrderID: 9, OrderTradeNo: "20260830T7K2M9Q4",
		InviterID: 2, InviteeID: 3, RateBps: 1000,
		BeforeAmount: 1590, BeforeStatus: "confirmed",
		AfterAmount: ptrOf(int64(1000)), AfterStatus: ptrOf("confirmed"),
		CreatedAt: tstz(time.Now()),
	}
	body := gen.CommissionAdjustRequest{Amount: -590, Reason: "退款套利，按 §5 追回部分佣金"}

	t.Run("正常路径：前后像与量纲都进审计", func(t *testing.T) {
		tx := &fakeCatalogTx{commissionRow: okRow}
		run := &fakeAuditRun{tx: tx}
		c, err := adjustAdminCommission(context.Background(), run.runner(), testActor(), 5, body)
		if err != nil {
			t.Fatal(err)
		}
		if c.Amount != 1000 {
			t.Fatalf("响应必须是改后金额，实际 %d", c.Amount)
		}
		if tx.commissionArg.DeltaAmount != -590 {
			t.Fatal("增量必须原样传下去")
		}
		e := run.lastEntry(t)
		if e.Action != "D11.commission.adjust" {
			t.Fatalf("action 不对：%q", e.Action)
		}
		if e.Before.(map[string]any)["amount"] != int64(1590) {
			t.Fatal("前像必须是改前金额")
		}
		if e.Before.(map[string]any)["amount_unit"] != "cent" {
			t.Fatal("量纲必须进审计 —— 一条只写着 1590 的记录事后分不清是 15.9 元还是 1590 元")
		}
		if e.Reason == "" {
			t.Fatal("D11 是 §6.2 L2 明列的操作，reason 必须进审计")
		}
	})

	// 「参数没收齐时不许提交」：D11 是 §6.2 L2 表里点名的操作。
	t.Run("🔴 reason < 8 字符 → 422，一条写入都没有", func(t *testing.T) {
		tx := &fakeCatalogTx{commissionRow: okRow}
		run := &fakeAuditRun{tx: tx}
		b := body
		b.Reason = "追回"
		if _, err := adjustAdminCommission(context.Background(), run.runner(), testActor(), 5, b); err == nil {
			t.Fatal("必须被拒")
		}
		if run.calls != 0 || tx.commissionArg != nil {
			t.Fatal("参数没收齐时不许提交")
		}
	})

	t.Run("amount = 0 → 422（0 调整只会往 append-only 表里写一条什么都没发生的记录）", func(t *testing.T) {
		tx := &fakeCatalogTx{}
		run := &fakeAuditRun{tx: tx}
		b := body
		b.Amount = 0
		if _, err := adjustAdminCommission(context.Background(), run.runner(), testActor(), 5, b); err == nil {
			t.Fatal("必须被拒")
		}
	})

	t.Run("不存在 → 404", func(t *testing.T) {
		tx := &fakeCatalogTx{commissionErr: pgx.ErrNoRows}
		run := &fakeAuditRun{tx: tx}
		_, err := adjustAdminCommission(context.Background(), run.runner(), testActor(), 5, body)
		oe, ok := asCatalogOpError(err)
		if !ok || oe.kind != catalogErrNotFound {
			t.Fatalf("应当是 404，实际 %v", err)
		}
	})

	// 🔴 404 与 409 必须分得开：一个是「你打错 id 了」，一个是「钱已经付出去了，去走冲正」。
	t.Run("🔴 已划转 / 已作废（after_amount 为 NULL）→ 409，且 message 带原状态", func(t *testing.T) {
		row := okRow
		row.AfterAmount = nil
		row.AfterStatus = nil
		row.BeforeStatus = "transferred"
		tx := &fakeCatalogTx{commissionRow: row}
		run := &fakeAuditRun{tx: tx}
		_, err := adjustAdminCommission(context.Background(), run.runner(), testActor(), 5, body)
		oe, ok := asCatalogOpError(err)
		if !ok || oe.kind != catalogErrConflict {
			t.Fatalf("应当是 409，实际 %v", err)
		}
		if !strings.Contains(oe.msg, "transferred") {
			t.Fatalf("message 必须带原状态，否则操作者不知道该走哪条路：%q", oe.msg)
		}
		if run.committed {
			t.Fatal("不能提交")
		}
	})

	t.Run("负向调过头（CHECK 23514）→ 422 而不是 500", func(t *testing.T) {
		tx := &fakeCatalogTx{commissionErr: pgErr("23514")}
		run := &fakeAuditRun{tx: tx}
		_, err := adjustAdminCommission(context.Background(), run.runner(), testActor(), 5, body)
		oe, ok := asCatalogOpError(err)
		if !ok || oe.kind != catalogErrUnprocessable {
			t.Fatalf("应当是 422，实际 %v", err)
		}
	})

	t.Run("🔴 审计写失败 → 不提交", func(t *testing.T) {
		tx := &fakeCatalogTx{commissionRow: okRow}
		run := &fakeAuditRun{tx: tx, auditErr: errors.New("boom")}
		if _, err := adjustAdminCommission(context.Background(), run.runner(), testActor(), 5, body); err == nil {
			t.Fatal("必须失败")
		}
		if run.committed {
			t.Fatal("不能提交")
		}
	})
}

// ============================================================
// 工单
// ============================================================

// 🔴 映射表必须写死。用 enum ordinal 隐式转换的话，将来插一个档位，
//
//	所有历史工单的 level 会在同一次部署里静默改变含义。
func TestTicketLevelPriorityMapping(t *testing.T) {
	table := map[dbgen.TicketPriority]int32{
		dbgen.TicketPriorityLow:    1,
		dbgen.TicketPriorityNormal: 2,
		dbgen.TicketPriorityHigh:   3,
		dbgen.TicketPriorityUrgent: 4,
	}
	for p, level := range table {
		if got := ticketLevelFromPriority(p); got != level {
			t.Fatalf("%s 应当是 %d，实际 %d", p, level, got)
		}
		back, err := ticketPriorityFromLevel(level)
		if err != nil || back != p {
			t.Fatalf("%d 应当映射回 %s，实际 %s（%v）", level, p, back, err)
		}
	}
	for _, bad := range []int32{0, 5, -1} {
		if _, err := ticketPriorityFromLevel(bad); err == nil {
			t.Fatalf("level=%d 必须被拒", bad)
		}
	}
	if ticketLevelFromPriority("critical") != 2 {
		t.Fatal("未知档位落到 normal（2），不是 0 —— 0 在契约里没有含义")
	}
}

// 🔴 replied 在库里没有对应的状态值：它由两个时间戳算出来。
//
//	静默映射成别的状态会让管理员以为自己标成了「已回复」，而实际状态是另一个。
func TestAdminTicketStatusToDB(t *testing.T) {
	if _, err := adminTicketStatusToDB(gen.Replied); err == nil {
		t.Fatal("🔴 replied 必须 422 并说清它是算出来的")
	}
	for in, want := range map[gen.TicketStatus]dbgen.TicketStatus{
		gen.Open:    dbgen.TicketStatusOpen,
		gen.Pending: dbgen.TicketStatusPending,
		gen.Closed:  dbgen.TicketStatusClosed,
	} {
		got, err := adminTicketStatusToDB(in)
		if err != nil || got != want {
			t.Fatalf("%s 应当映射成 %s，实际 %s（%v）", in, want, got, err)
		}
	}
	if _, err := adminTicketStatusToDB("archived"); err == nil {
		t.Fatal("未知状态必须被拒")
	}
}

func TestUpdateAdminTicket(t *testing.T) {
	base := func() *fakeCatalogTx {
		return &fakeCatalogTx{updateTicketRow: dbgen.AdminUpdateTicketRow{
			ID: 51, PublicID: "BP-7K2M9Q", Subject: "连不上",
			Status: dbgen.TicketStatusPending, Priority: dbgen.TicketPriorityHigh,
			BeforeStatus: dbgen.TicketStatusOpen, BeforePriority: dbgen.TicketPriorityNormal,
		}}
	}
	t.Run("只改等级时状态参数为 nil（SQL 里 coalesce 成现值）", func(t *testing.T) {
		tx := base()
		run := &fakeAuditRun{tx: tx}
		var warns []string
		if _, err := updateAdminTicket(context.Background(), run.runner(), testActor(), 51,
			gen.AdminTicketPatch{Level: ptrOf(int32(3))}, collectWarn(&warns)); err != nil {
			t.Fatal(err)
		}
		if tx.updateTicketArg.Status != nil {
			t.Fatal("没传 status 就必须是 nil")
		}
		if tx.updateTicketArg.Priority == nil || *tx.updateTicketArg.Priority != dbgen.TicketPriorityHigh {
			t.Fatal("level=3 → high")
		}
	})
	t.Run("空 PATCH → 422（否则只会写一条 before == after 的审计）", func(t *testing.T) {
		tx := base()
		run := &fakeAuditRun{tx: tx}
		var warns []string
		if _, err := updateAdminTicket(context.Background(), run.runner(), testActor(), 51,
			gen.AdminTicketPatch{}, collectWarn(&warns)); err == nil {
			t.Fatal("必须被拒")
		}
		if run.calls != 0 {
			t.Fatal("不该开事务")
		}
	})
	t.Run("工单不存在 → 404", func(t *testing.T) {
		tx := base()
		tx.updateTicketErr = pgx.ErrNoRows
		run := &fakeAuditRun{tx: tx}
		var warns []string
		_, err := updateAdminTicket(context.Background(), run.runner(), testActor(), 51,
			gen.AdminTicketPatch{Level: ptrOf(int32(2))}, collectWarn(&warns))
		oe, ok := asCatalogOpError(err)
		if !ok || oe.kind != catalogErrNotFound {
			t.Fatalf("应当是 404，实际 %v", err)
		}
	})
	t.Run("审计带 status 与 level 的前后值", func(t *testing.T) {
		tx := base()
		run := &fakeAuditRun{tx: tx}
		var warns []string
		if _, err := updateAdminTicket(context.Background(), run.runner(), testActor(), 51,
			gen.AdminTicketPatch{Status: ptrOf(gen.Pending), Level: ptrOf(int32(3))}, collectWarn(&warns)); err != nil {
			t.Fatal(err)
		}
		e := run.lastEntry(t)
		if e.Before.(map[string]any)["level"] != int32(2) || e.After.(map[string]any)["level"] != int32(3) {
			t.Fatal("等级的前后值都要进审计")
		}
	})
	t.Run("🔴 审计写失败 → 不提交", func(t *testing.T) {
		tx := base()
		run := &fakeAuditRun{tx: tx, auditErr: errors.New("boom")}
		var warns []string
		if _, err := updateAdminTicket(context.Background(), run.runner(), testActor(), 51,
			gen.AdminTicketPatch{Level: ptrOf(int32(2))}, collectWarn(&warns)); err == nil {
			t.Fatal("必须失败")
		}
		if run.committed {
			t.Fatal("不能提交")
		}
	})
}

func TestCreateAdminTicketMessage(t *testing.T) {
	base := func() *fakeCatalogTx {
		return &fakeCatalogTx{
			bumpRow: dbgen.AdminBumpTicketOnAgentMessageRow{ID: 61, PublicID: "BP-X", MessageCount: 4, BeforeMessageCount: 3},
			msgRow:  dbgen.TicketMessage{ID: 900, CreatedAt: tstz(time.Now())},
		}
	}

	// 🔴 内部备注不得推进 SLA 首次响应时钟。判据必须是 is_internal，
	//    而 AdminBumpTicketOnAgentMessage 就是为此存在的那条查询。
	t.Run("🔴 is_internal 必须传给 bump 查询（它按这个判 SLA 首次响应）", func(t *testing.T) {
		tx := base()
		run := &fakeAuditRun{tx: tx}
		_, err := createAdminTicketMessage(context.Background(), run.runner(), testActor(), 7, 61,
			gen.AdminCreateTicketMessageRequest{Message: "内部备注：等节点商回复", IsInternal: true})
		if err != nil {
			t.Fatal(err)
		}
		if tx.bumpArg == nil {
			t.Fatal("必须调 AdminBumpTicketOnAgentMessage（不是 BumpTicketMessageCount —— " +
				"后者按 actor_type 判，会让一句内部备注把 SLA 判为已达成，而用户一个字都没收到）")
		}
		if !tx.bumpArg.IsInternal {
			t.Fatal("is_internal 必须传下去")
		}
		if tx.msgArg.IsInternal != true {
			t.Fatal("消息本身也要标记内部")
		}
	})

	t.Run("🔴 is_internal 必须进审计（一条永不到达用户的备注与一条真回复是两回事）", func(t *testing.T) {
		tx := base()
		run := &fakeAuditRun{tx: tx}
		if _, err := createAdminTicketMessage(context.Background(), run.runner(), testActor(), 7, 61,
			gen.AdminCreateTicketMessageRequest{Message: "内部备注", IsInternal: true}); err != nil {
			t.Fatal(err)
		}
		after := run.lastEntry(t).After.(map[string]any)
		if after["is_internal"] != true {
			t.Fatal("is_internal 必须出现在审计的 after 里")
		}
	})

	// bump 先跑：0 行就是「工单不存在」的干净判据，404 不必依赖 INSERT 的外键错误码。
	t.Run("🔴 工单不存在 → 404，且消息没有被写入", func(t *testing.T) {
		tx := base()
		tx.bumpErr = pgx.ErrNoRows
		run := &fakeAuditRun{tx: tx}
		_, err := createAdminTicketMessage(context.Background(), run.runner(), testActor(), 7, 61,
			gen.AdminCreateTicketMessageRequest{Message: "你好"})
		oe, ok := asCatalogOpError(err)
		if !ok || oe.kind != catalogErrNotFound {
			t.Fatalf("应当是 404，实际 %v", err)
		}
		if tx.msgArg != nil {
			t.Fatal("工单不存在时不该写消息")
		}
	})

	t.Run("消息为空 / 超长 → 422，且没有任何写入", func(t *testing.T) {
		for name, m := range map[string]string{
			"空":  "   ",
			"超长": strings.Repeat("字", ticketMessageMaxRunes+1),
		} {
			tx := base()
			run := &fakeAuditRun{tx: tx}
			if _, err := createAdminTicketMessage(context.Background(), run.runner(), testActor(), 7, 61,
				gen.AdminCreateTicketMessageRequest{Message: m}); err == nil {
				t.Fatalf("%s 必须被拒", name)
			}
			if run.calls != 0 {
				t.Fatalf("%s：参数没收齐时不许提交", name)
			}
		}
	})

	t.Run("actor_type 恒为 agent，channel 是 admin", func(t *testing.T) {
		tx := base()
		run := &fakeAuditRun{tx: tx}
		if _, err := createAdminTicketMessage(context.Background(), run.runner(), testActor(), 7, 61,
			gen.AdminCreateTicketMessageRequest{Message: "已为你重置订阅"}); err != nil {
			t.Fatal(err)
		}
		if tx.msgArg.ActorType != dbgen.TicketActorAgent {
			t.Fatal("管理面写的消息 actor_type 恒为 agent")
		}
		if tx.msgArg.Channel != dbgen.TicketChannelAdmin {
			t.Fatal("channel 应当是 admin —— 用 web 会让「这条回复从哪来」事后不可分辨")
		}
		if tx.msgArg.AdminUserID == nil || *tx.msgArg.AdminUserID != 7 {
			t.Fatal("admin_user_id 必须写")
		}
	})

	t.Run("🔴 审计写失败 → 不提交（消息与 SLA 时钟一起回滚）", func(t *testing.T) {
		tx := base()
		run := &fakeAuditRun{tx: tx, auditErr: errors.New("boom")}
		if _, err := createAdminTicketMessage(context.Background(), run.runner(), testActor(), 7, 61,
			gen.AdminCreateTicketMessageRequest{Message: "你好"}); err == nil {
			t.Fatal("必须失败")
		}
		if run.committed {
			t.Fatal("不能提交")
		}
	})
}

type fakeTicketReader struct {
	row     dbgen.AdminGetTicketDetailRow
	rowErr  error
	msgs    []dbgen.TicketMessage
	msgsErr error
}

func (f *fakeTicketReader) AdminGetTicketDetail(context.Context, int64) (dbgen.AdminGetTicketDetailRow, error) {
	return f.row, f.rowErr
}
func (f *fakeTicketReader) ListTicketMessagesInternal(context.Context, int64) ([]dbgen.TicketMessage, error) {
	return f.msgs, f.msgsErr
}

func TestGetAdminTicket(t *testing.T) {
	t.Run("管理面看得到内部备注（用户面的类型上根本没有这个字段）", func(t *testing.T) {
		f := &fakeTicketReader{
			row: dbgen.AdminGetTicketDetailRow{
				ID: 1, PublicID: "BP-X", UserID: 9, UserEmail: "u@example.com",
				Subject: "连不上", Status: dbgen.TicketStatusOpen, Priority: dbgen.TicketPriorityNormal,
				CategorySlug: ptrOf("node-down"), Context: []byte(`{"plan":"标准"}`),
			},
			msgs: []dbgen.TicketMessage{
				{ID: 1, ActorType: dbgen.TicketActorUser, Body: "连不上"},
				{ID: 2, ActorType: dbgen.TicketActorAgent, Body: "内部：节点商在处理", IsInternal: true},
				{ID: 3, ActorType: dbgen.TicketActorSystem, Body: "已转技术组"},
			},
		}
		var warns []string
		d, err := getAdminTicket(context.Background(), f, 1, collectWarn(&warns))
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Messages) != 3 {
			t.Fatalf("内部备注也要给，实际 %d 条", len(d.Messages))
		}
		if !d.Messages[1].IsInternal {
			t.Fatal("is_internal 必须原样下发")
		}
		if d.Messages[2].Author != gen.AdminTicketMessageAuthorStaff {
			t.Fatal("system 映射成 staff（契约只有 user/staff）")
		}
		if d.Context == nil || (*d.Context)["plan"] != "标准" {
			t.Fatal("诊断快照必须解出来")
		}
		if string(d.UserEmail) != "u@example.com" {
			t.Fatal("user_email 必须给")
		}
	})
	t.Run("工单不存在 → 404", func(t *testing.T) {
		f := &fakeTicketReader{rowErr: pgx.ErrNoRows}
		var warns []string
		_, err := getAdminTicket(context.Background(), f, 1, collectWarn(&warns))
		oe, ok := asCatalogOpError(err)
		if !ok || oe.kind != catalogErrNotFound {
			t.Fatalf("应当是 404，实际 %v", err)
		}
	})
	t.Run("没有分类时给 account 并留 WARN（契约的 category 是 required）", func(t *testing.T) {
		f := &fakeTicketReader{row: dbgen.AdminGetTicketDetailRow{ID: 1, CategorySlug: nil}}
		var warns []string
		d, err := getAdminTicket(context.Background(), f, 1, collectWarn(&warns))
		if err != nil {
			t.Fatal(err)
		}
		if d.Ticket.Category != gen.Account || len(warns) == 0 {
			t.Fatal("必须给一个契约内的值并留痕")
		}
	})
}

// ============================================================
// 审计日志
// ============================================================

type fakeAuditLister struct {
	rows     []dbgen.AuditLog
	arg      dbgen.ListAdminAuditLogPageParams
	countArg dbgen.CountAdminAuditLogParams
	total    int64
	counted  bool
}

func (f *fakeAuditLister) ListAdminAuditLogPage(_ context.Context, arg dbgen.ListAdminAuditLogPageParams) ([]dbgen.AuditLog, error) {
	f.arg = arg
	return f.rows, nil
}
func (f *fakeAuditLister) CountAdminAuditLog(_ context.Context, arg dbgen.CountAdminAuditLogParams) (int64, error) {
	f.counted = true
	f.countArg = arg
	return f.total, nil
}

func TestListAdminAuditLog(t *testing.T) {
	now := time.Now()
	row := dbgen.AuditLog{
		ID: 1, AdminUserID: ptrOf(int64(7)), AdminEmailSnapshot: "ops@example.com",
		Action: "D6.order.mark_paid", TargetType: "order", TargetID: "20260830T7",
		BeforeValue: []byte(`{"status":"pending"}`), AfterValue: []byte(`{"status":"paid"}`),
		Reason: ptrOf("链上已确认"), RequestIp: netip.MustParseAddr("203.0.113.9"),
		CreatedAt: tstz(now),
	}

	t.Run("🔴 action 过滤是包含匹配，且列表与 COUNT 用同一个模式", func(t *testing.T) {
		f := &fakeAuditLister{rows: []dbgen.AuditLog{row}}
		var warns []string
		_, _, err := listAdminAuditLog(context.Background(), f, gen.Meta{}, gen.ListAdminAuditLogParams{
			Action: ptrOf("order.mark_paid"),
			Count:  ptrOf(gen.CountQuery(true)),
		}, collectWarn(&warns))
		if err != nil {
			t.Fatal(err)
		}
		if f.arg.ActionLike == nil || !strings.Contains(*f.arg.ActionLike, "%") {
			t.Fatal("必须是包含匹配 —— 库里 action 带 D 编号前缀，等值一条都查不到且不报错")
		}
		if f.countArg.ActionLike == nil || *f.countArg.ActionLike != *f.arg.ActionLike {
			t.Fatal("🔴 COUNT 的 WHERE 必须与列表逐字同形，否则「共 87 条」与列表说的是两件事")
		}
	})

	t.Run("🔴 两列游标 (created_at, id)", func(t *testing.T) {
		f := &fakeAuditLister{rows: []dbgen.AuditLog{row, row}}
		var warns []string
		_, meta, _ := listAdminAuditLog(context.Background(), f, gen.Meta{},
			gen.ListAdminAuditLogParams{Limit: ptrOf(gen.LimitQuery(1))}, collectWarn(&warns))
		if meta.NextCursor == nil {
			t.Fatal("应当有游标")
		}
		cur, ok := decodePageCursor(*meta.NextCursor)
		if !ok || cur.ID != 1 || cur.At.IsZero() {
			t.Fatal("游标必须同时带 created_at 与 id —— 同一事务里写的多条审计时间戳可以完全相同")
		}
	})

	// request_id 在契约里是 required，而 audit_logs 没有这一列。
	t.Run("🔴 request_id 只能是空串（编一个会让人以为它能接回那次操作）", func(t *testing.T) {
		f := &fakeAuditLister{rows: []dbgen.AuditLog{row}}
		var warns []string
		data, _, _ := listAdminAuditLog(context.Background(), f, gen.Meta{}, gen.ListAdminAuditLogParams{}, collectWarn(&warns))
		if data[0].RequestId != "" {
			t.Fatal("audit_logs 没有 request_id 列，只能填空串")
		}
		if data[0].Before == nil || (*data[0].Before)["status"] != "pending" {
			t.Fatal("before 必须解成对象")
		}
	})

	t.Run("admin_user_id 为 NULL 时填 0 并留 WARN（那条记录从此指认不到人）", func(t *testing.T) {
		r := row
		r.AdminUserID = nil
		f := &fakeAuditLister{rows: []dbgen.AuditLog{r}}
		var warns []string
		data, _, _ := listAdminAuditLog(context.Background(), f, gen.Meta{}, gen.ListAdminAuditLogParams{}, collectWarn(&warns))
		if data[0].AdminId != 0 {
			t.Fatal("NULL 只能填 0")
		}
		if !hasSubstr(warns, "admin_email_snapshot") {
			t.Fatal("必须留 WARN —— admin_email_snapshot 在契约的 AuditLogEntry 上没有字段可放，只能进日志")
		}
	})

	t.Run("target_type 过滤是等值（不是包含）", func(t *testing.T) {
		f := &fakeAuditLister{rows: []dbgen.AuditLog{row}}
		var warns []string
		_, _, _ = listAdminAuditLog(context.Background(), f, gen.Meta{},
			gen.ListAdminAuditLogParams{TargetType: ptrOf("order")}, collectWarn(&warns))
		if f.arg.TargetType == nil || *f.arg.TargetType != "order" {
			t.Fatal("target_type 是枚举式的短串，等值匹配就对了")
		}
	})
}

// ============================================================
// 系统配置（D13）
// ============================================================

type fakeSettingsReader struct {
	rows []dbgen.Setting
	err  error
}

func (f *fakeSettingsReader) ListAdminSettings(context.Context) ([]dbgen.Setting, error) {
	return f.rows, f.err
}

func TestReadAdminSettings(t *testing.T) {
	t.Run("jsonb 解成对应的 Go 值", func(t *testing.T) {
		f := &fakeSettingsReader{rows: []dbgen.Setting{
			{Key: "register_open", Value: []byte(`true`)},
			{Key: "sla_hours", Value: []byte(`24`)},
		}}
		var warns []string
		m, err := readAdminSettings(context.Background(), f, collectWarn(&warns))
		if err != nil {
			t.Fatal(err)
		}
		if m["register_open"] != true || m["sla_hours"] != float64(24) {
			t.Fatalf("值没有正确解出：%v", m)
		}
	})
	t.Run("空表返回空 map 而不是 nil（nil 序列化成 null）", func(t *testing.T) {
		var warns []string
		m, _ := readAdminSettings(context.Background(), &fakeSettingsReader{}, collectWarn(&warns))
		if m == nil {
			t.Fatal("必须是非 nil 的 map")
		}
	})
	t.Run("坏值不丢弃（丢一个键会让人去「新建」它，而写侧是 UPDATE-only）", func(t *testing.T) {
		f := &fakeSettingsReader{rows: []dbgen.Setting{{Key: "broken", Value: []byte(`{not json`)}}}
		var warns []string
		m, _ := readAdminSettings(context.Background(), f, collectWarn(&warns))
		if _, ok := m["broken"]; !ok {
			t.Fatal("键必须保留")
		}
		if len(warns) == 0 {
			t.Fatal("必须留 WARN")
		}
	})
}

func TestUpdateAdminSettings(t *testing.T) {
	body := gen.SettingsPatchRequest{
		Reason: "把注册开关关掉，等域名迁移完成再开",
		Values: gen.SettingsMap{"register_open": false, "sla_hours": 24},
	}

	t.Run("🔴 键必须排序（map 遍历顺序随机 → 审计 diff 没法看、问题无从复现）", func(t *testing.T) {
		tx := &fakeCatalogTx{settingsRows: []dbgen.UpdateAdminSettingsValuesRow{
			{Key: "register_open", BeforeValue: []byte(`true`), AfterValue: []byte(`false`)},
			{Key: "sla_hours", BeforeValue: []byte(`48`), AfterValue: []byte(`24`)},
		}}
		run := &fakeAuditRun{tx: tx}
		if err := updateAdminSettings(context.Background(), run.runner(), testActor(), 7, body); err != nil {
			t.Fatal(err)
		}
		got := tx.settingsArg.SettingKeys
		if len(got) != 2 || got[0] != "register_open" || got[1] != "sla_hours" {
			t.Fatalf("键必须按字典序，实际 %v", got)
		}
		// 两个数组按下标配对，顺序必须对得上。
		if tx.settingsArg.SettingValues[0] != "false" || tx.settingsArg.SettingValues[1] != "24" {
			t.Fatalf("值必须与键同序，实际 %v", tx.settingsArg.SettingValues)
		}
		if tx.settingsArg.AdminUserID != 7 {
			t.Fatal("updated_by 必须是当前管理员")
		}
	})

	// 🔴 纯 UPDATE 不 UPSERT：一次手滑的键名会影响 0 行，
	//    不断言的话页面显示「已保存」而行为没有任何变化。
	t.Run("🔴 有键不存在 → 422 并列出不认识的键", func(t *testing.T) {
		tx := &fakeCatalogTx{settingsRows: []dbgen.UpdateAdminSettingsValuesRow{
			{Key: "sla_hours", BeforeValue: []byte(`48`), AfterValue: []byte(`24`)},
		}}
		run := &fakeAuditRun{tx: tx}
		err := updateAdminSettings(context.Background(), run.runner(), testActor(), 7, body)
		oe, ok := asCatalogOpError(err)
		if !ok || oe.kind != catalogErrUnprocessable {
			t.Fatalf("应当是 422，实际 %v", err)
		}
		if !strings.Contains(oe.msg, "register_open") {
			t.Fatalf("必须说清是哪个键不认识：%q", oe.msg)
		}
		if run.committed {
			t.Fatal("不能提交")
		}
	})

	t.Run("🔴 reason < 8 字符 → 422，一条写入都没有", func(t *testing.T) {
		tx := &fakeCatalogTx{}
		run := &fakeAuditRun{tx: tx}
		b := body
		b.Reason = "改配置"
		if err := updateAdminSettings(context.Background(), run.runner(), testActor(), 7, b); err == nil {
			t.Fatal("必须被拒")
		}
		if run.calls != 0 || tx.settingsArg != nil {
			t.Fatal("参数没收齐时不许提交")
		}
	})

	t.Run("空 values / 超量 → 422", func(t *testing.T) {
		tx := &fakeCatalogTx{}
		run := &fakeAuditRun{tx: tx}
		b := body
		b.Values = gen.SettingsMap{}
		if err := updateAdminSettings(context.Background(), run.runner(), testActor(), 7, b); err == nil {
			t.Fatal("空 values 必须被拒")
		}
		big := gen.SettingsMap{}
		for i := 0; i <= settingsMaxKeys; i++ {
			big[strings.Repeat("k", i%50+1)+string(rune('a'+i%26))+strconvItoa(i)] = 1
		}
		b2 := body
		b2.Values = big
		if err := updateAdminSettings(context.Background(), run.runner(), testActor(), 7, b2); err == nil {
			t.Fatal("超量必须被拒")
		}
	})

	// D13 要求「展示 diff」：审计必须是逐键的新旧值，不是「改了 5 个键」这种摘要。
	t.Run("🔴 审计是逐键的前后值（配置回滚时唯一能用的东西）", func(t *testing.T) {
		tx := &fakeCatalogTx{settingsRows: []dbgen.UpdateAdminSettingsValuesRow{
			{Key: "register_open", BeforeValue: []byte(`true`), AfterValue: []byte(`false`)},
			{Key: "sla_hours", BeforeValue: []byte(`48`), AfterValue: []byte(`24`)},
		}}
		run := &fakeAuditRun{tx: tx}
		if err := updateAdminSettings(context.Background(), run.runner(), testActor(), 7, body); err != nil {
			t.Fatal(err)
		}
		e := run.lastEntry(t)
		before := e.Before.(map[string]any)
		if string(before["register_open"].(json.RawMessage)) != "true" {
			t.Fatal("每个键的旧值都要在")
		}
		after := e.After.(map[string]any)
		if string(after["sla_hours"].(json.RawMessage)) != "24" {
			t.Fatal("每个键的新值都要在")
		}
		if e.Action != "D13.settings.update" || e.TargetType != "settings" {
			t.Fatalf("审计头不对：%s/%s", e.Action, e.TargetType)
		}
		if !strings.Contains(e.TargetID, "register_open") {
			t.Fatalf("target_id 应当是被改的键，实际 %q", e.TargetID)
		}
	})

	t.Run("🔴 审计写失败 → 不提交", func(t *testing.T) {
		tx := &fakeCatalogTx{settingsRows: []dbgen.UpdateAdminSettingsValuesRow{
			{Key: "register_open", BeforeValue: []byte(`true`), AfterValue: []byte(`false`)},
			{Key: "sla_hours", BeforeValue: []byte(`48`), AfterValue: []byte(`24`)},
		}}
		run := &fakeAuditRun{tx: tx, auditErr: errors.New("boom")}
		if err := updateAdminSettings(context.Background(), run.runner(), testActor(), 7, body); err == nil {
			t.Fatal("必须失败")
		}
		if run.committed {
			t.Fatal("不能提交")
		}
	})
}

// strconvItoa 是给上面那个「超量」用例造键名用的小工具（避免引入额外 import 的歧义）。
func strconvItoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// targetIDMaxBytes 的截断必须退到 rune 边界 ——
// text 列拒收非法 UTF-8，切在多字节字符中间会让整条审计写失败 → 业务一起回滚。
func TestTruncateForTargetID(t *testing.T) {
	s := strings.Repeat("配置项", 500) // 每个 3 字节
	got := truncateForTargetID(s)
	if len(got) > targetIDMaxBytes+len("…") {
		t.Fatalf("必须截断，实际 %d 字节", len(got))
	}
	if !isValidUTF8(got) {
		t.Fatal("🔴 截断结果必须是合法 UTF-8")
	}
	short := "a,b"
	if truncateForTargetID(short) != short {
		t.Fatal("不超长的串原样返回")
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

// ============================================================
// 流量统计与导出（D14）
// ============================================================

func TestParseStatsWindow(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	t.Run("都不传 → 最近 30 天", func(t *testing.T) {
		w, err := parseStatsWindow(nil, nil, now)
		if err != nil {
			t.Fatal(err)
		}
		if !w.to.Equal(now) {
			t.Fatalf("to 缺省是 now，实际 %s", w.to)
		}
		if w.to.Sub(w.from) != time.Duration(statsDefaultWindowDays)*24*time.Hour {
			t.Fatalf("默认窗口应当是 %d 天", statsDefaultWindowDays)
		}
	})
	t.Run("只传 from（另一端补齐，不该报错）", func(t *testing.T) {
		from := now.AddDate(0, 0, -7)
		if _, err := parseStatsWindow(&from, nil, now); err != nil {
			t.Fatalf("只传 from 是合法请求：%v", err)
		}
	})
	t.Run("to <= from → 422", func(t *testing.T) {
		if _, err := parseStatsWindow(&now, &now, now); err == nil {
			t.Fatal("必须被拒")
		}
	})
	t.Run("跨度超上限 → 422（这个端点没有分页参数，更长的窗口只会被截断）", func(t *testing.T) {
		from := now.AddDate(-3, 0, 0)
		if _, err := parseStatsWindow(&from, &now, now); err == nil {
			t.Fatal("必须被拒")
		}
	})
}

type fakeStatsQuerier struct {
	global    []dbgen.GetGlobalDailyTrafficRow
	byUser    []dbgen.ListAdminStatByUserRow
	byServer  []dbgen.ListAdminStatByServerRow
	userArg   dbgen.ListAdminStatByUserParams
	globalArg dbgen.GetGlobalDailyTrafficParams
	err       error
}

func (f *fakeStatsQuerier) GetGlobalDailyTraffic(_ context.Context, arg dbgen.GetGlobalDailyTrafficParams) ([]dbgen.GetGlobalDailyTrafficRow, error) {
	f.globalArg = arg
	return f.global, f.err
}
func (f *fakeStatsQuerier) ListAdminStatByUser(_ context.Context, arg dbgen.ListAdminStatByUserParams) ([]dbgen.ListAdminStatByUserRow, error) {
	f.userArg = arg
	return f.byUser, f.err
}
func (f *fakeStatsQuerier) ListAdminStatByServer(_ context.Context, arg dbgen.ListAdminStatByServerParams) ([]dbgen.ListAdminStatByServerRow, error) {
	return f.byServer, f.err
}

func TestLoadAdminStats(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	w := statsWindow{from: now.AddDate(0, 0, -7), to: now}
	d := pgtype.Date{Time: time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC), Valid: true}

	t.Run("global：走 Shanghai 日期，record_at 由同一个 helper 换算", func(t *testing.T) {
		f := &fakeStatsQuerier{global: []dbgen.GetGlobalDailyTrafficRow{{StatDate: d, U: 10, D: 20}}}
		out, truncated, err := loadAdminStats(context.Background(), f, "global", w)
		if err != nil {
			t.Fatal(err)
		}
		if truncated {
			t.Fatal("global 维度每天一行，不会触顶")
		}
		if !f.globalArg.StatDate.Valid {
			t.Fatal("必须传 Shanghai 切出来的 date")
		}
		if !out[0].RecordAt.Equal(catalogRecordAt(d)) {
			t.Fatal("record_at 必须走同一个换算函数")
		}
		if out[0].UserId != nil || out[0].ServerId != nil {
			t.Fatal("global 维度不带 user_id / server_id")
		}
	})

	// 🔴 端点没有分页参数，静默截断的报表会被当成完整数据去做决策。
	t.Run("🔴 user：命中行数上限时报告截断", func(t *testing.T) {
		rows := make([]dbgen.ListAdminStatByUserRow, statsPageLimit)
		for i := range rows {
			rows[i] = dbgen.ListAdminStatByUserRow{StatDate: d, UserID: int64(i + 1)}
		}
		f := &fakeStatsQuerier{byUser: rows}
		out, truncated, err := loadAdminStats(context.Background(), f, "user", w)
		if err != nil {
			t.Fatal(err)
		}
		if !truncated {
			t.Fatal("必须报告截断")
		}
		if len(out) != statsPageLimit || out[0].UserId == nil {
			t.Fatal("user 维度必须带 user_id")
		}
		if f.userArg.PageLimit != statsPageLimit {
			t.Fatal("必须钉死上限")
		}
	})

	t.Run("server：带 server_id，不带 user_id", func(t *testing.T) {
		f := &fakeStatsQuerier{byServer: []dbgen.ListAdminStatByServerRow{{StatDate: d, ServerID: 3, UploadBytes: 1}}}
		out, _, err := loadAdminStats(context.Background(), f, "server", w)
		if err != nil {
			t.Fatal(err)
		}
		if out[0].ServerId == nil || *out[0].ServerId != 3 || out[0].UserId != nil {
			t.Fatal("server 维度的字段不对")
		}
	})

	t.Run("未知 scope → 422", func(t *testing.T) {
		if _, _, err := loadAdminStats(context.Background(), &fakeStatsQuerier{}, "galaxy", w); err == nil {
			t.Fatal("必须被拒")
		}
	})

	t.Run("默认 scope 是 global", func(t *testing.T) {
		if statScope(nil) != "global" || statScope(ptrOf("")) != "global" {
			t.Fatal("契约的 default 是 global")
		}
	})
}

func TestStatsCSV(t *testing.T) {
	rows := []gen.StatBucket{
		{RecordAt: time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC), UserId: ptrOf(int64(7)), UploadBytes: 10, DownloadBytes: 20},
	}
	b, err := statsCSV(rows, "user")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.HasPrefix(s, "\xEF\xBB\xBF") {
		t.Fatal("必须带 UTF-8 BOM（这份文件会被 Excel 打开）")
	}
	if !strings.Contains(s, "record_at,scope,user_id,server_id,upload_bytes,download_bytes") {
		t.Fatal("表头必须固定 —— 下游脚本不该按 scope 分支解析")
	}
	if !strings.Contains(s, "2026-08-29T16:00:00Z,user,7,,10,20") {
		t.Fatalf("行内容不对：%q", s)
	}
	// 🔴 不 JOIN 邮箱：加一列 email 会让这个端点从「流量数字」变成「用户名单」。
	if strings.Contains(strings.ToLower(s), "email") {
		t.Fatal("统计导出不能带邮箱")
	}
}

// ============================================================
// 运营看板
// ============================================================

type fakeDashboardQuerier struct {
	failOnline  bool
	failNodes   bool
	failRevenue bool
}

func (f *fakeDashboardQuerier) GetAdminDashboardOnlineUsers(context.Context) (dbgen.GetAdminDashboardOnlineUsersRow, error) {
	if f.failOnline {
		return dbgen.GetAdminDashboardOnlineUsersRow{}, errors.New("UNLOGGED 表刚被 TRUNCATE")
	}
	return dbgen.GetAdminDashboardOnlineUsersRow{OnlineUsers: 42, OnlineDevices: 90}, nil
}
func (f *fakeDashboardQuerier) GetAdminDashboardNodes(context.Context) (dbgen.GetAdminDashboardNodesRow, error) {
	if f.failNodes {
		return dbgen.GetAdminDashboardNodesRow{}, errors.New("boom")
	}
	return dbgen.GetAdminDashboardNodesRow{TotalNodes: 13, EnabledNodes: 12, AliveNodes: 10}, nil
}
func (f *fakeDashboardQuerier) GetAdminDashboardTrafficToday(context.Context) (dbgen.GetAdminDashboardTrafficTodayRow, error) {
	return dbgen.GetAdminDashboardTrafficTodayRow{TodayUploadBytes: 1, TodayDownloadBytes: 2}, nil
}
func (f *fakeDashboardQuerier) GetAdminDashboardRevenue(context.Context) (dbgen.GetAdminDashboardRevenueRow, error) {
	if f.failRevenue {
		return dbgen.GetAdminDashboardRevenueRow{}, errors.New("orders 顺序扫描超时")
	}
	return dbgen.GetAdminDashboardRevenueRow{TodayRevenueAmount: 100, MonthRevenueAmount: 3000}, nil
}
func (f *fakeDashboardQuerier) GetAdminDashboardQueues(context.Context) (dbgen.GetAdminDashboardQueuesRow, error) {
	return dbgen.GetAdminDashboardQueuesRow{PendingTickets: 4, UnderpaidOrders: 1}, nil
}

func TestLoadAdminDashboard(t *testing.T) {
	t.Run("正常路径：active_nodes 映射 alive_nodes（不是 enabled_nodes）", func(t *testing.T) {
		d := loadAdminDashboard(context.Background(), &fakeDashboardQuerier{}, func(string, error) {})
		if d.ActiveNodes == nil || *d.ActiveNodes != 10 {
			t.Fatal("🔴 管理员打开看板是想知道现在有几个**能用的**，那是 alive_nodes")
		}
		if d.TotalNodes == nil || *d.TotalNodes != 13 {
			t.Fatal("total_nodes 不对")
		}
		if d.OnlineUsers == nil || *d.OnlineUsers != 42 {
			t.Fatal("online_users 不对")
		}
		if d.MonthRevenueAmount == nil || *d.MonthRevenueAmount != 3000 {
			t.Fatal("month_revenue 不对")
		}
	})

	// 🔴 在线数来自 UNLOGGED 表，收入要顺序扫 orders —— 它们的失败模式与
	//    「数一下未读工单」完全不同。任一格失败不该让整页 500。
	t.Run("🔴 一格失败 → 那一格缺字段（前端渲染「—」），其余四格照常", func(t *testing.T) {
		var failed []string
		d := loadAdminDashboard(context.Background(), &fakeDashboardQuerier{failOnline: true, failRevenue: true},
			func(cell string, _ error) { failed = append(failed, cell) })
		if d.OnlineUsers != nil {
			t.Fatal("失败的格必须缺字段而不是 0 —— 0 是一个会被当真的数字")
		}
		if d.TodayRevenueAmount != nil || d.MonthRevenueAmount != nil {
			t.Fatal("收入两格都该缺")
		}
		if d.ActiveNodes == nil || d.PendingTickets == nil || d.TodayUploadBytes == nil {
			t.Fatal("其余三格必须照常渲染")
		}
		if len(failed) != 2 {
			t.Fatalf("两格失败都要留痕，实际 %v", failed)
		}
	})

	t.Run("并发度必须等于连接池 max（ADR 0005）", func(t *testing.T) {
		if dashboardConcurrency != 2 {
			t.Fatalf("并发度必须是 2，实际 %d —— 开五个 goroutine 只会让三个在池上排队",
				dashboardConcurrency)
		}
	})
}

// ============================================================
// 邮件（D11b 群发 · 送达日志）
// ============================================================

func TestBroadcastAudienceToDB(t *testing.T) {
	for _, a := range []gen.MailBroadcastRequestAudience{
		gen.MailBroadcastRequestAudienceAll,
		gen.MailBroadcastRequestAudienceActive,
		gen.MailBroadcastRequestAudienceExpired,
		gen.MailBroadcastRequestAudienceExpiringSoon,
	} {
		if _, ids, err := broadcastAudienceToDB(a, nil); err != nil || ids == nil {
			t.Fatalf("%s 应当通过且 plan_ids 是非 nil 空数组（NOT NULL 列）：%v", a, err)
		}
	}
	t.Run("by_plan 没给 plan_ids → 422（否则 SQL 里 = ANY('{}') 恒为 false，命中 0 人）", func(t *testing.T) {
		if _, _, err := broadcastAudienceToDB(gen.MailBroadcastRequestAudienceByPlan, nil); err == nil {
			t.Fatal("必须被拒")
		}
		empty := []int64{}
		if _, _, err := broadcastAudienceToDB(gen.MailBroadcastRequestAudienceByPlan, &empty); err == nil {
			t.Fatal("空数组也必须被拒")
		}
	})
	// 🔴 SQL 里 ELSE false 不是装饰：反过来写意味着一次拼错的枚举 = 一次全站群发。
	t.Run("🔴 未知 audience → 422（不能落到「全部」）", func(t *testing.T) {
		if _, _, err := broadcastAudienceToDB("everyone", nil); err == nil {
			t.Fatal("必须被拒")
		}
	})
}

// 🔴 模板键白名单：verify_code / expire_remind 这些**必须**发不出去。
func TestBroadcastTemplateAllowlist(t *testing.T) {
	if !broadcastTemplates[broadcastTemplateDomain] {
		t.Fatal("域名广播必须允许 —— 它是 ADR 0002 唯一的失联恢复通道")
	}
	for _, k := range []string{
		emailTemplateVerifyCode, // 群发它 = 给全站用户各发一封凭据邮件
		templateExpireRemind,    // 幂等键是 (user_id, template, 当天)，手工群发会吃掉当天配额
		templateTrafficRemind,
		"", "custom", "任意正文",
	} {
		if broadcastTemplates[k] {
			t.Fatalf("%q 绝不能出现在群发白名单里", k)
		}
	}
}

func TestBroadcastAdminMail(t *testing.T) {
	body := gen.MailBroadcastRequest{
		Subject:  "域名切换通知",
		Body:     broadcastTemplateDomain,
		Audience: gen.MailBroadcastRequestAudienceAll,
		Reason:   "主域名被墙，按 ADR 0002 走失联恢复广播",
	}

	t.Run("正常路径：queued 取入队语句的实际行数，不是确认框那个 count", func(t *testing.T) {
		tx := &fakeCatalogTx{
			audienceCount: 312,
			enqueueRows: []dbgen.AdminEnqueueBroadcastMailsRow{
				{ID: 1, ToDomain: "qq.com"}, {ID: 2, ToDomain: "qq.com"}, {ID: 3, ToDomain: "163.com"},
			},
		}
		run := &fakeAuditRun{tx: tx}
		queued, expected, err := broadcastAdminMail(context.Background(), run.runner(), testActor(), body, broadcastTemplateDomain, "ses")
		if err != nil {
			t.Fatal(err)
		}
		if expected != 312 {
			t.Fatalf("命中人数应当是 312，实际 %d", expected)
		}
		if queued != 3 {
			t.Fatalf("🔴 queued 必须是入队语句的行数（3），实际 %d —— "+
				"确认框那个数字与真正入队之间隔着管理员点确认的几秒", queued)
		}
		if tx.enqueueArg.Template != broadcastTemplateDomain || tx.enqueueArg.Esp != "ses" {
			t.Fatal("模板键与 esp 必须传下去")
		}
		e := run.lastEntry(t)
		if e.Action != "D11b.mail.broadcast" {
			t.Fatalf("action 不对：%q", e.Action)
		}
		after := e.After.(map[string]any)
		if after["expected_recipients"] != int64(312) || after["queued"] != int64(3) {
			t.Fatal("两个数字都要进审计")
		}
		byDomain := after["by_domain"].(map[string]int)
		if byDomain["qq.com"] != 2 {
			t.Fatal("按域名分组的收件数必须进审计 —— 退信率是按域名看的")
		}
	})

	// 🔴 命中 0 人正是那个确认数字要防的意外。
	t.Run("🔴 命中 0 人 → 422，且不入队", func(t *testing.T) {
		tx := &fakeCatalogTx{audienceCount: 0}
		run := &fakeAuditRun{tx: tx}
		_, _, err := broadcastAdminMail(context.Background(), run.runner(), testActor(), body, broadcastTemplateDomain, "ses")
		oe, ok := asCatalogOpError(err)
		if !ok || oe.kind != catalogErrUnprocessable {
			t.Fatalf("应当是 422，实际 %v", err)
		}
		if tx.enqueueArg != nil {
			t.Fatal("不该入队")
		}
	})

	t.Run("🔴 reason < 8 字符 → 422，一次查询都没跑", func(t *testing.T) {
		tx := &fakeCatalogTx{audienceCount: 100}
		run := &fakeAuditRun{tx: tx}
		b := body
		b.Reason = "通知"
		if _, _, err := broadcastAdminMail(context.Background(), run.runner(), testActor(), b, broadcastTemplateDomain, "ses"); err == nil {
			t.Fatal("必须被拒")
		}
		if run.calls != 0 {
			t.Fatal("参数没收齐时不许提交")
		}
	})

	t.Run("空主题 / 超长主题 → 422", func(t *testing.T) {
		for name, subj := range map[string]string{
			"空":  "   ",
			"超长": strings.Repeat("字", broadcastSubjectMaxRunes+1),
		} {
			tx := &fakeCatalogTx{audienceCount: 100}
			run := &fakeAuditRun{tx: tx}
			b := body
			b.Subject = subj
			if _, _, err := broadcastAdminMail(context.Background(), run.runner(), testActor(), b, broadcastTemplateDomain, "ses"); err == nil {
				t.Fatalf("%s 主题必须被拒", name)
			}
		}
	})

	t.Run("🔴 审计写失败 → 不提交（那批信不会被发出去）", func(t *testing.T) {
		tx := &fakeCatalogTx{audienceCount: 10, enqueueRows: []dbgen.AdminEnqueueBroadcastMailsRow{{ID: 1, ToDomain: "qq.com"}}}
		run := &fakeAuditRun{tx: tx, auditErr: errors.New("boom")}
		if _, _, err := broadcastAdminMail(context.Background(), run.runner(), testActor(), body, broadcastTemplateDomain, "ses"); err == nil {
			t.Fatal("必须失败")
		}
		if run.committed {
			t.Fatal("不能提交")
		}
	})

	t.Run("expiring_soon 带服务端钉的窗口（契约里没有这个参数）", func(t *testing.T) {
		tx := &fakeCatalogTx{audienceCount: 5, enqueueRows: []dbgen.AdminEnqueueBroadcastMailsRow{{ID: 1, ToDomain: "qq.com"}}}
		run := &fakeAuditRun{tx: tx}
		b := body
		b.Audience = gen.MailBroadcastRequestAudienceExpiringSoon
		if _, _, err := broadcastAdminMail(context.Background(), run.runner(), testActor(), b, broadcastTemplateDomain, "ses"); err != nil {
			t.Fatal(err)
		}
		if tx.audienceArg.ExpiringWithin.Days != broadcastExpiringWithinDays {
			t.Fatalf("窗口应当是 %d 天，实际 %d", broadcastExpiringWithinDays, tx.audienceArg.ExpiringWithin.Days)
		}
		// 🔴 两处 WHERE 是刻意的重复，参数必须逐字相同 ——
		//    漂移的现象是「确认框说 312 人，实际发给了 1100 人」。
		if tx.enqueueArg.ExpiringWithin.Days != tx.audienceArg.ExpiringWithin.Days ||
			tx.enqueueArg.Audience != tx.audienceArg.Audience {
			t.Fatal("命中人数与入队必须用同一组筛选参数")
		}
	})
}

// 🔴 契约把 sent_at 放在 required 里，而库里这一列可空。
// 把 NULL 序列化成零值时间 → 1970-01-01 出现在送达率报表里，
// 会被当成一封「很久以前就发了但没到」的信。
func TestAdminMailLogView(t *testing.T) {
	created := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	sent := time.Date(2026, 8, 30, 10, 0, 5, 0, time.UTC)

	t.Run("🔴 sent_at 为 NULL 时回落到 created_at（不是 1970）", func(t *testing.T) {
		v := adminMailLogView(dbgen.EmailLog{ID: 1, ToDomain: "qq.com", CreatedAt: tstz(created), Status: "queued"})
		if !v.SentAt.Equal(created) {
			t.Fatalf("必须回落到 created_at，实际 %s", v.SentAt)
		}
	})
	t.Run("有 sent_at 时用它", func(t *testing.T) {
		v := adminMailLogView(dbgen.EmailLog{
			ID: 1, ToDomain: "qq.com", CreatedAt: tstz(created), SentAt: tstz(sent),
			Esp: "ses", Template: "domain_broadcast", BounceCode: ptrOf("554 HL:IPB"),
		})
		if !v.SentAt.Equal(sent) {
			t.Fatal("有值就用值")
		}
		if v.Esp == nil || *v.Esp != "ses" || v.TemplateKey == nil {
			t.Fatal("esp / template_key 必须下发")
		}
		if v.BounceCode == nil {
			t.Fatal("退信码必须下发 —— 它是送达率排查的第一手材料")
		}
	})
}

type fakeMailLogLister struct {
	rows     []dbgen.EmailLog
	arg      dbgen.AdminListMailLogsPageParams
	total    int64
	countArg *string
}

func (f *fakeMailLogLister) AdminListMailLogsPage(_ context.Context, arg dbgen.AdminListMailLogsPageParams) ([]dbgen.EmailLog, error) {
	f.arg = arg
	return f.rows, nil
}
func (f *fakeMailLogLister) AdminCountMailLogsFiltered(_ context.Context, d *string) (int64, error) {
	f.countArg = d
	return f.total, nil
}

func TestListAdminMailLogs(t *testing.T) {
	created := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	rows := []dbgen.EmailLog{
		{ID: 2, ToDomain: "qq.com", CreatedAt: tstz(created)},
		{ID: 1, ToDomain: "qq.com", CreatedAt: tstz(created)},
	}

	t.Run("游标用 created_at（sent_at 可空，用它会在 queued 的信上产生 NULL 分量）", func(t *testing.T) {
		f := &fakeMailLogLister{rows: rows}
		var warns []string
		_, meta, err := listAdminMailLogs(context.Background(), f, gen.Meta{},
			gen.ListAdminMailLogsParams{Limit: ptrOf(gen.LimitQuery(1))}, collectWarn(&warns))
		if err != nil {
			t.Fatal(err)
		}
		cur, ok := decodePageCursor(*meta.NextCursor)
		if !ok || !cur.At.Equal(created) {
			t.Fatal("游标的 at 必须是 created_at")
		}
	})

	t.Run("🔴 列表与 COUNT 的过滤值必须是同一个", func(t *testing.T) {
		f := &fakeMailLogLister{rows: rows[:1], total: 9}
		var warns []string
		_, _, _ = listAdminMailLogs(context.Background(), f, gen.Meta{}, gen.ListAdminMailLogsParams{
			RecipientDomain: ptrOf("  qq.com  "),
			Count:           ptrOf(gen.CountQuery(true)),
		}, collectWarn(&warns))
		if f.arg.RecipientDomain == nil || *f.arg.RecipientDomain != "qq.com" {
			t.Fatalf("必须 trim 之后传下去，实际 %v", f.arg.RecipientDomain)
		}
		if f.countArg == nil || *f.countArg != *f.arg.RecipientDomain {
			t.Fatal("COUNT 的 WHERE 必须与列表逐字同形")
		}
	})

	t.Run("空过滤值等于没传（而不是过滤成空串）", func(t *testing.T) {
		f := &fakeMailLogLister{rows: rows[:1]}
		var warns []string
		_, _, _ = listAdminMailLogs(context.Background(), f, gen.Meta{},
			gen.ListAdminMailLogsParams{RecipientDomain: ptrOf("   ")}, collectWarn(&warns))
		if f.arg.RecipientDomain != nil {
			t.Fatal("只有空白等于没传 —— 过滤成空串会返回 0 行且不报错")
		}
	})
}

// ============================================================
// D14 的 L4：这是本组唯一一道**真的**权限位
// ============================================================

// 🔴 `perm_export_csv` 是 admin_users 上真实存在的一列，默认 false，
// **即使团队只有一个人也不预授**（api-contract §6.2 L4）。
// 本组其余的写操作只能靠角色推（admin.*.write 在库里没有列），
// 而这一条不同 —— 用例把这个区别钉住。
func TestExportStatsPermissionGateIsARealBit(t *testing.T) {
	t.Run("零值管理员没有导出权限（默认不授予）", func(t *testing.T) {
		var a middleware.AdminAuth
		if a.Can(middleware.PermExportCSV) {
			t.Fatal("默认必须不授予 —— D14 是数据泄漏面")
		}
	})
	t.Run("owner 角色本身不等于有导出权限（它是独立权限位，不由角色推）", func(t *testing.T) {
		a := middleware.AdminAuth{Role: middleware.RoleOwner}
		if a.Can(middleware.PermExportCSV) {
			t.Fatal("🔴 角色不能替代独立权限位 —— 那正是 L4 与本组其余闸门的区别")
		}
	})
	t.Run("显式授予后放行", func(t *testing.T) {
		a := middleware.AdminAuth{Perms: middleware.AdminPerms{ExportCSV: true}}
		if !a.Can(middleware.PermExportCSV) {
			t.Fatal("授予了就该放行")
		}
	})
	// 端点没有 from/to，窗口必须由服务端自钉 —— 无界导出会让 L4 白设。
	if statsExportWindowDays <= 0 || statsExportWindowDays > 366 {
		t.Fatalf("导出窗口必须有界且合理，实际 %d 天", statsExportWindowDays)
	}
	if statsExportPerHour <= 0 {
		t.Fatal("导出必须有频率上限（契约给这个端点声明了 429）")
	}
}

// 群发的频率上限：契约的 summary 逐字写着「限流 2/h」。
func TestMailBroadcastRateLimitMatchesContract(t *testing.T) {
	if mailBroadcastPerHour != 2 {
		t.Fatalf("契约写的是 2/h，实际 %d —— 退信率 ≥ 5%% 进入审查、≥ 10%% 可能暂停发信，"+
			"而邮件是唯一的失联恢复通道", mailBroadcastPerHour)
	}
}

// 🔴 空 jsonb 字节必须换成 null 字面量：长度 0 的 json.RawMessage 会让
// json.Marshal 报错 → 审计写失败 → 一次合法的配置修改被整体回滚。
func TestCatalogRawJSON(t *testing.T) {
	if string(catalogRawJSON(nil)) != "null" {
		t.Fatal("空字节必须变成 null 字面量")
	}
	if string(catalogRawJSON([]byte{})) != "null" {
		t.Fatal("零长切片同样")
	}
	// 原始字节必须原样保留：往返一趟会把 1.0 变成 1，
	// 而 D13 的审计是回滚配置的唯一依据 —— 要写回去的是当时那个字节串。
	raw := []byte(`{"a":1.0}`)
	if string(catalogRawJSON(raw)) != `{"a":1.0}` {
		t.Fatal("非空字节必须原样保留")
	}
	b, err := json.Marshal(map[string]any{"k": catalogRawJSON(nil)})
	if err != nil {
		t.Fatalf("包起来之后必须可序列化（否则审计写不进去）：%v", err)
	}
	if string(b) != `{"k":null}` {
		t.Fatalf("实际 %s", b)
	}
}
