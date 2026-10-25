package catalog

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/onprs/OCS-Tiku/internal/platform/config"
	"github.com/onprs/OCS-Tiku/internal/platform/models"
)

// Input 是面板提交的可编辑字段；公开编号、版本与时间戳由服务生成。
type Input struct {
	DisplayName  string      `json:"display_name"`
	Description  string      `json:"description"`
	ProviderRef  string      `json:"provider_ref"`
	Capabilities []string    `json:"capabilities"`
	Pricing      *Pricing    `json:"pricing"`
	CachePolicy  CachePolicy `json:"cache_policy"`
	Enabled      bool        `json:"enabled"`
	Default      bool        `json:"is_default"`
	SortOrder    int         `json:"sort_order"`
}

// Service 把数据库中的公开条目与题库配置模型合并，并派生健康状态。
// 主平台读取同一份事实，题库面板是唯一写入入口。
type Service struct {
	cfg    *config.Config
	models *models.Registry
	store  *Store
}

func New(cfg *config.Config, pool *pgxpool.Pool) *Service {
	return NewWithRegistry(cfg, pool, models.New(cfg.Models, nil))
}

// NewWithRegistry 使用与作答服务、维护面板共享的模型快照。
func NewWithRegistry(cfg *config.Config, pool *pgxpool.Pool, registry *models.Registry) *Service {
	return &Service{cfg: cfg, models: registry, store: NewStore(pool)}
}

func (s *Service) List(ctx context.Context) ([]Offering, error) {
	items, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	overrides, err := s.store.modelOverrides(ctx)
	if err != nil {
		return nil, err
	}
	for i := range items {
		items[i].Health = s.health(items[i].ProviderRef, overrides)
	}
	return items, nil
}

func (s *Service) Get(ctx context.Context, publicID string) (*Offering, error) {
	item, err := s.store.Get(ctx, publicID)
	if err != nil {
		return nil, err
	}
	overrides, err := s.store.modelOverrides(ctx)
	if err != nil {
		return nil, err
	}
	item.Health = s.health(item.ProviderRef, overrides)
	return item, nil
}

func (s *Service) Create(ctx context.Context, actor string, input Input) (*Offering, error) {
	item, err := s.build(nil, input)
	if err != nil {
		return nil, err
	}
	if item.Enabled && s.healthFor(ctx, item.ProviderRef) == HealthUnavailable {
		return nil, invalid("模型当前不可用，不能发布为可见条目")
	}
	created, err := s.store.Create(ctx, item, actor)
	if err != nil {
		return nil, err
	}
	created.Health = s.healthFor(ctx, created.ProviderRef)
	return created, nil
}

func (s *Service) Update(ctx context.Context, actor, publicID string, version int64, input Input) (*Offering, error) {
	current, err := s.store.Get(ctx, publicID)
	if err != nil {
		return nil, err
	}
	item, err := s.build(current, input)
	if err != nil {
		return nil, err
	}
	if item.Enabled && s.healthFor(ctx, item.ProviderRef) == HealthUnavailable {
		return nil, invalid("模型当前不可用，不能发布为可见条目")
	}
	updated, err := s.store.Update(ctx, item, version, actor)
	if err != nil {
		return nil, err
	}
	updated.Health = s.healthFor(ctx, updated.ProviderRef)
	return updated, nil
}

func (s *Service) PriceVersions(ctx context.Context, publicID string) ([]PriceVersion, error) {
	if _, err := s.store.Get(ctx, publicID); err != nil {
		return nil, err
	}
	return s.store.PriceVersions(ctx, publicID)
}

// Retire 关闭条目的可见性与默认状态；即使配置模型已被移除也可执行。
func (s *Service) Retire(ctx context.Context, publicID string, version int64) (*Offering, error) {
	item, err := s.store.Retire(ctx, publicID, version)
	if err != nil {
		return nil, err
	}
	item.Health = s.healthFor(ctx, item.ProviderRef)
	return item, nil
}

// ModelProfile 返回公开能力校验所需的模型事实；模型不存在时拒绝发布。
func (s *Service) ModelProfile(providerRef string) (ModelProfile, error) {
	for _, item := range s.models.Models() {
		if item.ID == providerRef {
			return ModelProfile{Vision: item.Vision}, nil
		}
	}
	return ModelProfile{}, invalid("模型引用不存在或未在题库配置中声明")
}

func (s *Service) build(current *Offering, input Input) (*Offering, error) {
	item := &Offering{
		DisplayName:  input.DisplayName,
		Description:  input.Description,
		ProviderRef:  input.ProviderRef,
		Capabilities: input.Capabilities,
		Pricing:      input.Pricing,
		CachePolicy:  input.CachePolicy,
		Enabled:      input.Enabled,
		Default:      input.Default,
		SortOrder:    input.SortOrder,
	}
	if current != nil {
		item.ID = current.ID
		item.PublicID = current.PublicID
	}
	profile, err := s.ModelProfile(item.ProviderRef)
	if err != nil {
		return nil, err
	}
	if configured := s.models.Model(item.ProviderRef); configured != nil && configured.Disabled && item.Enabled {
		return nil, invalid("该模型已被题库配置停用，不能发布为可见条目")
	}
	if err := item.Normalize(profile, s.cfg.Cache.MaxTTLDays, s.cfg.Cache.SharedEnabled); err != nil {
		return nil, err
	}
	if current == nil {
		item.ID = newUUID()
		item.PublicID = NewPublicID()
	}
	return item, nil
}

// healthFor 读取面板停用覆盖；读取失败时退回配置状态，不影响已提交的写入结果。
func (s *Service) healthFor(ctx context.Context, providerRef string) string {
	overrides, err := s.store.modelOverrides(ctx)
	if err != nil {
		overrides = nil
	}
	return s.health(providerRef, overrides)
}

// health 依据配置模型与面板停用覆盖派生健康状态；不写入数据库。
func (s *Service) health(providerRef string, overrides map[string]bool) string {
	configured := false
	disabled := false
	if item := s.models.Model(providerRef); item != nil {
		configured = true
		disabled = item.Disabled
	}
	if !configured {
		return HealthUnavailable
	}
	if overrides == nil {
		overrides = map[string]bool{}
	}
	if disabled || overrides[providerRef] {
		return HealthUnavailable
	}
	return HealthConfigured
}
