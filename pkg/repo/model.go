package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mylxsw/aidea-server/internal/coins"
	"github.com/mylxsw/aidea-server/pkg/repo/model"
	"github.com/mylxsw/asteria/log"
	"github.com/mylxsw/eloquent"
	"github.com/mylxsw/eloquent/query"
	"github.com/mylxsw/go-utils/array"
	"gopkg.in/guregu/null.v3"
	"math/rand"
	"strings"
)

type ModelRepo struct {
	db *sql.DB
}

func NewModelRepo(db *sql.DB) *ModelRepo {
	return &ModelRepo{db: db}
}

type Model struct {
	model.Models
	Meta      ModelMeta       `json:"meta,omitempty"`
	Providers []ModelProvider `json:"providers,omitempty"`
}

func (m Model) ToCoinModel() coins.ModelInfo {
	return coins.ModelInfo{
		ModelId:     m.ModelId,
		InputPrice:  m.Meta.InputPrice,
		OutputPrice: m.Meta.OutputPrice,
	}
}

func (m Model) SelectProvider() ModelProvider {
	if len(m.Providers) == 0 {
		return ModelProvider{Name: "openai"}
	}

	// TODO 更好的选择策略
	return m.Providers[rand.Intn(len(m.Providers))]
}

const (
	ModelStatusEnabled  int64 = 1
	ModelStatusDisabled int64 = 2
)

func NewModel(m model.Models) Model {
	ret := Model{Models: m}

	if ret.ShortName == "" {
		ret.ShortName = ret.Name
	}

	if ret.MetaJson != "" {
		if err := json.Unmarshal([]byte(ret.MetaJson), &ret.Meta); err != nil {
			log.F(log.M{"model": ret}).Errorf("unmarshal model meta failed: %s", err)
		}

		// 没有设置输出价格，但是设置了输入价格，则输出价格与输入价格相同
		if ret.Meta.OutputPrice == 0 {
			ret.Meta.OutputPrice = ret.Meta.InputPrice
		}

		// 没有设置输入价格，但是设置了输出价格，则输入价格与输出价格相同
		if ret.Meta.InputPrice == 0 {
			ret.Meta.InputPrice = ret.Meta.OutputPrice
		}
	}

	if ret.ProvidersJson != "" {
		if err := json.Unmarshal([]byte(ret.ProvidersJson), &ret.Providers); err != nil {
			log.F(log.M{"model": ret}).Errorf("unmarshal model providers failed: %s", err)
		}
	}

	return ret
}

type ModelMeta struct {
	// Vision 是否支持视觉能力
	Vision bool `json:"vision,omitempty"`
	// Restricted 是否是受限制的模型
	Restricted bool `json:"restricted,omitempty"`
	// MaxContext 最大上下文长度
	MaxContext int `json:"max_context,omitempty"`
	// InputPrice 输入 Token 价格（智慧果/1K Token），为空则与 OutputPrice 相同
	InputPrice int `json:"input_price,omitempty"`
	// OutputPrice 输出 Token 价格（智慧果/1K Token）
	OutputPrice int `json:"output_price,omitempty"`

	// Prompt 全局的系统提示语
	Prompt string `json:"prompt,omitempty"`
}

type ModelProvider struct {
	// 模型供应商 ID 优先从 channels 中查询模型供应商，不设置则根据 name 直接读取配置文件中固定的供应商配置
	ID int64 `json:"id,omitempty"`
	// Name 供应商名称
	Name string `json:"name,omitempty"`
	// ModelRewrite 模型名称重写，如果为空，则使用模型的名称
	ModelRewrite string `json:"model_rewrite,omitempty"`
}

// SupportProvider check if the model support the provider
func (m Model) SupportProvider(providerName string) *ModelProvider {
	for _, p := range m.Providers {
		if p.Name == providerName && p.ID <= 0 {
			return &p
		}
	}

	return nil
}

func (m Model) SupportDynamicProvider() bool {
	for _, p := range m.Providers {
		if p.ID > 0 {
			return true
		}
	}

	return false

}

// GetModels return all models
func (repo *ModelRepo) GetModels(ctx context.Context, options ...QueryOption) ([]Model, error) {
	q := query.Builder()
	for _, opt := range options {
		q = opt(q)
	}

	models, err := model.NewModelsModel(repo.db).Get(ctx, q)
	if err != nil {
		return nil, err
	}

	return array.Map(models, func(m model.ModelsN, _ int) Model {
		return NewModel(m.ToModels())
	}), nil
}

// GetModel return model by modelID
func (repo *ModelRepo) GetModel(ctx context.Context, modelID string) (*Model, error) {
	m, err := model.NewModelsModel(repo.db).First(ctx, query.Builder().Where(model.FieldModelsModelId, modelID))
	if err != nil {
		if errors.Is(err, query.ErrNoResult) {
			return nil, ErrNotFound
		}

		return nil, err
	}

	ret := NewModel(m.ToModels())
	return &ret, nil
}

type ModelAddReq struct {
	ModelID     string          `json:"model_id,omitempty"`
	Name        string          `json:"name,omitempty"`
	ShortName   string          `json:"short_name,omitempty"`
	Description string          `json:"description,omitempty"`
	AvatarUrl   string          `json:"avatar_url,omitempty"`
	Status      int64           `json:"status,omitempty"`
	Meta        ModelMeta       `json:"meta,omitempty"`
	Providers   []ModelProvider `json:"providers,omitempty"`
}

// AddModel 添加模型
func (repo *ModelRepo) AddModel(ctx context.Context, req ModelAddReq) (int64, error) {
	meta, _ := json.Marshal(req.Meta)
	providers, _ := json.Marshal(req.Providers)

	if req.Status == 0 {
		req.Status = ModelStatusEnabled
	}

	var id int64
	err := eloquent.Transaction(repo.db, func(tx query.Database) error {
		exists, err := model.NewModelsModel(tx).Exists(ctx, query.Builder().Where(model.FieldModelsModelId, req.ModelID))
		if err != nil {
			return err
		}

		if exists {
			return errors.New("model already exists")
		}

		insertID, err := model.NewModelsModel(tx).Create(ctx, query.KV{
			model.FieldModelsModelId:       req.ModelID,
			model.FieldModelsName:          req.Name,
			model.FieldModelsShortName:     req.ShortName,
			model.FieldModelsDescription:   req.Description,
			model.FieldModelsAvatarUrl:     req.AvatarUrl,
			model.FieldModelsStatus:        req.Status,
			model.FieldModelsMetaJson:      string(meta),
			model.FieldModelsProvidersJson: string(providers),
		})
		if err != nil {
			return err
		}

		id = insertID
		return nil
	})

	return id, err
}

type ModelUpdateReq struct {
	Name        string          `json:"name,omitempty"`
	ShortName   string          `json:"short_name,omitempty"`
	Description string          `json:"description,omitempty"`
	AvatarUrl   string          `json:"avatar_url,omitempty"`
	Status      int64           `json:"status,omitempty"`
	VersionMin  string          `json:"version_min,omitempty"`
	VersionMax  string          `json:"version_max,omitempty"`
	Meta        ModelMeta       `json:"meta,omitempty"`
	Providers   []ModelProvider `json:"providers,omitempty"`
}

// UpdateModel 更新模型信息
func (repo *ModelRepo) UpdateModel(ctx context.Context, modelID string, req ModelUpdateReq) error {
	mod, err := model.NewModelsModel(repo.db).First(ctx, query.Builder().Where(model.FieldModelsModelId, modelID))
	if err != nil {
		if errors.Is(err, query.ErrNoResult) {
			return ErrNotFound
		}

		return err
	}

	mod.Name = null.StringFrom(req.Name)
	mod.ShortName = null.StringFrom(req.ShortName)
	mod.Description = null.StringFrom(req.Description)
	mod.AvatarUrl = null.StringFrom(req.AvatarUrl)
	mod.Status = null.IntFrom(req.Status)

	meta, _ := json.Marshal(req.Meta)
	mod.MetaJson = null.StringFrom(string(meta))

	providers, _ := json.Marshal(req.Providers)
	mod.ProvidersJson = null.StringFrom(string(providers))

	return mod.Save(ctx)
}

// DeleteModel 删除模型
func (repo *ModelRepo) DeleteModel(ctx context.Context, modelID string) error {
	_, err := model.NewModelsModel(repo.db).Delete(ctx, query.Builder().Where(model.FieldModelsModelId, modelID))
	return err
}

type Channel struct {
	model.Channels
	Meta ChannelMeta `json:"meta,omitempty"`
}

type ChannelMeta struct {
	// UsingProxy 是否使用系统代理
	UsingProxy bool `json:"using_proxy,omitempty"`
	// OpenAIAzure 是否使用 OpenAI 的 Azure 服务
	OpenAIAzure bool `json:"openai_azure,omitempty"`
	// OpenAIAzureAPIVersion OpenAI Azure API 版本
	OpenAIAzureAPIVersion string `json:"openai_azure_api_version,omitempty"`
}

func NewChannel(ch model.ChannelsN) Channel {
	ret := Channel{Channels: ch.ToChannels()}
	if ret.MetaJson != "" {
		if err := json.Unmarshal([]byte(ret.MetaJson), &ret.Meta); err != nil {
			log.F(log.M{"model": ret}).Errorf("unmarshal channel meta failed: %s", err)
		}
	}

	return ret
}

// GetChannels 返回所有的渠道
func (repo *ModelRepo) GetChannels(ctx context.Context) ([]Channel, error) {
	channels, err := model.NewChannelsModel(repo.db).Get(ctx, query.Builder())
	if err != nil {
		return nil, err
	}

	return array.Map(channels, func(m model.ChannelsN, _ int) Channel {
		return NewChannel(m)
	}), nil
}

// GetChannel 返回指定的渠道
func (repo *ModelRepo) GetChannel(ctx context.Context, id int64) (*Channel, error) {
	ch, err := model.NewChannelsModel(repo.db).First(ctx, query.Builder().Where(model.FieldChannelsId, id))
	if err != nil {
		if errors.Is(err, query.ErrNoResult) {
			return nil, ErrNotFound
		}

		return nil, err
	}

	ret := NewChannel(*ch)
	return &ret, nil
}

type ChannelUpdateReq struct {
	Name   string      `json:"name"`
	Type   string      `json:"type"`
	Server string      `json:"server,omitempty"`
	Secret string      `json:"secret,omitempty"`
	Meta   ChannelMeta `json:"meta,omitempty"`
}

// UpdateChannel 更新渠道信息
func (repo *ModelRepo) UpdateChannel(ctx context.Context, id int64, req ChannelUpdateReq) error {
	ch, err := model.NewChannelsModel(repo.db).First(ctx, query.Builder().Where(model.FieldChannelsId, id))
	if err != nil {
		if errors.Is(err, query.ErrNoResult) {
			return ErrNotFound
		}

		return err
	}

	ch.Name = null.StringFrom(req.Name)
	ch.Type = null.StringFrom(req.Type)
	ch.Server = null.StringFrom(req.Server)
	ch.Secret = null.StringFrom(req.Secret)

	meta, _ := json.Marshal(req.Meta)
	ch.MetaJson = null.StringFrom(string(meta))

	return ch.Save(ctx)
}

type ChannelAddReq struct {
	Name   string      `json:"name"`
	Type   string      `json:"type"`
	Server string      `json:"server,omitempty"`
	Secret string      `json:"secret,omitempty"`
	Meta   ChannelMeta `json:"meta,omitempty"`
}

// AddChannel 添加渠道
func (repo *ModelRepo) AddChannel(ctx context.Context, req ChannelAddReq) (int64, error) {
	meta, _ := json.Marshal(req.Meta)

	return model.NewChannelsModel(repo.db).Create(ctx, query.KV{
		model.FieldChannelsName:     req.Name,
		model.FieldChannelsType:     req.Type,
		model.FieldChannelsServer:   req.Server,
		model.FieldChannelsSecret:   req.Secret,
		model.FieldChannelsMetaJson: string(meta),
	})
}

// DeleteChannel 删除渠道
func (repo *ModelRepo) DeleteChannel(ctx context.Context, channelID int64) error {
	models, err := repo.GetModels(ctx)
	if err != nil {
		return err
	}

	relatedModels := array.Filter(models, func(item Model, _ int) bool {
		for _, provider := range item.Providers {
			if provider.ID == channelID {
				return true
			}
		}
		return false
	})

	if len(relatedModels) > 0 {
		return fmt.Errorf(
			"当前渠道下有关联的模型，无法删除：%s (%w)",
			strings.Join(array.Map(relatedModels, func(item Model, _ int) string { return item.Name }), ","),
			ErrViolationOfBusinessConstraint,
		)
	}

	_, err = model.NewChannelsModel(repo.db).Delete(ctx, query.Builder().Where(model.FieldChannelsId, channelID))
	return err
}
