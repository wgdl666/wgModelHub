package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
	"github.com/wgdl666/wgModelHub/models"
)

const (
	BootstrapFilePath = "/etc/wg-model-hub/bootstrap.json"
	NacosDataID       = "wg.mirror.modelHub"
	NacosGroup        = "DEFAULT_GROUP"

	CapabilityText   = "text"
	CapabilityImage  = "image"
	CapabilityVideo  = "video"
	CapabilitySpeech = "speech"
	CapabilityASR    = "asr"
)

// Bootstrap 只保存 Nacos 定位信息；供应商凭据只能存在于受保护的配置正文中。
type FashionProviderConfig struct {
	BaseURL  string `yaml:"base_url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type Bootstrap struct {
	ServerAddress string `json:"server_address"`
	NamespaceID   string `json:"namespace_id"`
}

// ProviderConfig 用互斥嵌套字段表达具体供应商；每个实例必须且只能配置一种，
// 并显式声明该实例承载的真实模型 ID 列表（含版本）。
type ProviderConfig struct {
	// ImageSeg 保留 Muse 的阿里云抠图，不以 BRIA 替换算法。
	ImageSeg *FacebodyCompareProviderConfig `yaml:"image_seg"`

	// Fashion 保留现有衣橱向量空间和多模态精排协议。
	Fashion *FashionProviderConfig `yaml:"fashion"`

	Models         []string                      `yaml:"models"`
	Gemini         *GeminiProviderConfig         `yaml:"gemini"`
	VertexAI       *VertexAIProviderConfig       `yaml:"vertexai"`
	Ark            *ArkProviderConfig            `yaml:"ark"`
	OpenAI         *OpenAIProviderConfig         `yaml:"openai"`
	LTX            *LTXProviderConfig            `yaml:"ltx"`
	DashScopeVideo *DashScopeVideoProviderConfig `yaml:"dashscope_video"`
	OminilinkVideo *OminilinkVideoProviderConfig `yaml:"ominilink_video"`
	GeminiVideo    *GeminiVideoProviderConfig    `yaml:"gemini_video"`
	ArkVideo       *ArkVideoProviderConfig       `yaml:"ark_video"`
	// MinimaxTTS 承接同步一次性 TTS；与 chat/completions OpenAI 实例分绑，避免误用文本能力。
	MinimaxTTS *MinimaxTTSProviderConfig `yaml:"minimax_tts"`
	// ElevenLabsTTS 承接整段和流式 TTS；与 Minimax 分绑，未配置时不影响现有 speech 路由。
	ElevenLabsTTS *ElevenLabsTTSProviderConfig `yaml:"elevenlabs_tts"`
	// 八类实时 ASR 各自独立配置，避免把厂家字段泄漏进统一 RPC，也不与 TTS Speech 共用实例。
	VolcengineASR *VolcengineASRProviderConfig `yaml:"volcengine_asr"`
	AliyunASR     *AliyunASRProviderConfig     `yaml:"aliyun_asr"`
	FunASR        *FunASRProviderConfig        `yaml:"fun_asr"`
	QwenASR       *QwenASRProviderConfig       `yaml:"qwen_asr"`
	TencentASR    *TencentASRProviderConfig    `yaml:"tencent_asr"`
	AssemblyAIASR *AssemblyAIASRProviderConfig `yaml:"assemblyai_asr"`
	DeepgramASR   *DeepgramASRProviderConfig   `yaml:"deepgram_asr"`
	SonioxASR     *SonioxASRProviderConfig     `yaml:"soniox_asr"`
	// Photoroom 承接官方 Remove Background（POST /v1/segment）；与 OpenAI/Gemini 生图实例分绑，禁止共用。
	Photoroom *PhotoroomProviderConfig `yaml:"photoroom"`
	// SegmentPerson 承接国内自建人物/商品主体抠图；与 Photoroom 分实例，靠 models 列表切换。
	SegmentPerson *SegmentPersonProviderConfig `yaml:"segment_person"`
	// VWorldImage 承接薇光点亮公网 FLUX 拆衣服/虚拟换衣；与 SeeTacloud flux2_klein_image 分实例，禁止共用。
	VWorldImage *VWorldImageProviderConfig `yaml:"vworld_image"`
	// HumanYOLO 承接自建人体检测。和 Rekognition、人体解析分实例。
	HumanYOLO *HumanYOLOProviderConfig `yaml:"human_yolo"`
	// HumanParser 承接人体解析，输出分割 JSON 而不是检测框。
	// 同一结构服务两个 ID：human-parser 走旧 /predict，human_parse 走 G7 /human_parser/predict。
	HumanParser *HumanParserProviderConfig `yaml:"human_parser"`
	// RekognitionDetect 承接 DetectLabels 人检。Region 不能是 cn-*。
	RekognitionDetect *RekognitionDetectProviderConfig `yaml:"rekognition_detect"`
	// FacebodyCompare 承接阿里云 1:1 人脸比对。与 Rekognition 比对分实例，调用方用模型 ID 选择。
	FacebodyCompare *FacebodyCompareProviderConfig `yaml:"facebody_compare"`
	// RekognitionCompare 承接 CompareFaces。与 DetectLabels 人检分实例，Region 不能是 cn-*。
	RekognitionCompare *RekognitionCompareProviderConfig `yaml:"rekognition_compare"`
	// DashScopeEmbedding 承接 qwen3-vl-embedding。维度固定 1024。
	DashScopeEmbedding *DashScopeEmbeddingProviderConfig `yaml:"dashscope_embedding"`
	// CohereEmbedding 承接官方 embed-v4.0，和 Bedrock 推理配置分实例。
	CohereEmbedding *CohereEmbeddingProviderConfig `yaml:"cohere_embedding"`
	// BedrockEmbedding 承接 us.cohere.embed-v4:0。role_arn 为空时用任务角色。
	BedrockEmbedding *BedrockEmbeddingProviderConfig `yaml:"bedrock_embedding"`
	// DashScopeRerank 承接 qwen3.7-text-rerank 和 qwen3-vl-rerank。
	DashScopeRerank *DashScopeRerankProviderConfig `yaml:"dashscope_rerank"`
	// BedrockRerank 承接 cohere.rerank-v3-5:0。instruct 不会发给该模型。
	BedrockRerank *BedrockRerankProviderConfig `yaml:"bedrock_rerank"`
	// CohereRerank 承接官网 rerank-v3.5。请求不带 instruct。
	CohereRerank *CohereRerankProviderConfig `yaml:"cohere_rerank"`
	// MixedbreadRerank 承接 mixedbread-ai/mxbai-rerank-v3.1-listwise。没有 instruct 字段，query 里的颜色词在供应商内收成主色检索句。
	MixedbreadRerank *MixedbreadRerankProviderConfig `yaml:"mixedbread_rerank"`
	// FacebodyDetect 承接 DetectFace，只返回脸框。
	FacebodyDetect *FacebodyDetectProviderConfig `yaml:"facebody_detect"`
	// FacebodyLibrary 承接人脸库查重、录脸、删脸。database 是 Facebody 库名。
	FacebodyLibrary *FacebodyLibraryProviderConfig `yaml:"facebody_library"`
	// RekognitionFaces 承接 DetectFaces，和 DetectLabels 人检分实例。
	RekognitionFaces *RekognitionFacesProviderConfig `yaml:"rekognition_faces"`
	// RekognitionLibrary 承接 Collection 查重、录脸、删脸。
	RekognitionLibrary *RekognitionLibraryProviderConfig `yaml:"rekognition_library"`
}

type GeminiProviderConfig struct {
	APIKey   string `yaml:"api_key"`
	BaseURL  string `yaml:"base_url"`
	ProxyURL string `yaml:"proxy_url"`
}

// VertexAIProviderConfig 使用 Vertex Express API key。
// 文本走 aiplatform.googleapis.com 的 publishers 短路径，不带 project。
// Project 只服务 Nano Banana 2.1：该模型只在 locations/global，官方 Express 短路径会落到调用方区域。
// SDK 不允许 APIKey 与 Project/Location 同时设置，所以 global 客户端另建，密钥仍是这把 api_key，不走 ADC。
// Location 固定 global，不进配置。缺 project 时该模型在请求时返回配置错误，不探测。
type VertexAIProviderConfig struct {
	APIKey string `yaml:"api_key"`
	// Project 是 Vertex 项目编号或 ID。空表示只走 Express 短路径。
	Project string `yaml:"project"`
}

type VolcengineASRProviderConfig struct {
	AppKey     string `yaml:"app_key"`
	AccessKey  string `yaml:"access_key"`
	APIKey     string `yaml:"api_key"`
	ResourceID string `yaml:"resource_id"`
	URL        string `yaml:"url"`
}

type AliyunASRProviderConfig struct {
	AKID   string `yaml:"ak_id"`
	AKKey  string `yaml:"ak_key"`
	AppKey string `yaml:"app_key"`
	Token  string `yaml:"token"`
	URL    string `yaml:"url"`
}

type FunASRProviderConfig struct {
	APIKey      string `yaml:"api_key"`
	WorkspaceID string `yaml:"workspace_id"`
	URL         string `yaml:"url"`
}

type QwenASRProviderConfig struct {
	APIKey      string `yaml:"api_key"`
	WorkspaceID string `yaml:"workspace_id"`
	URL         string `yaml:"url"`
}

type TencentASRProviderConfig struct {
	AppID     string `yaml:"app_id"`
	SecretID  string `yaml:"secret_id"`
	SecretKey string `yaml:"secret_key"`
	ProxyURL  string `yaml:"proxy_url"`
}

type AssemblyAIASRProviderConfig struct {
	APIKey string `yaml:"api_key"`
	URL    string `yaml:"url"`
}

type DeepgramASRProviderConfig struct {
	APIKey string `yaml:"api_key"`
	URL    string `yaml:"url"`
}

type SonioxASRProviderConfig struct {
	APIKey string `yaml:"api_key"`
	URL    string `yaml:"url"`
}

type ArkProviderConfig struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
	// EndpointID 可选：火山方舟推理部署 ID（如 ep-xxx）。对外 request.model 仍是 models 包常量，仅上游 Responses 请求体改用此 endpoint。
	EndpointID string `yaml:"endpoint_id"`
}

// OpenAIProviderConfig 覆盖 OpenAI-compatible HTTP 端；文本实例与 GPT Image（async_gpt_image 上的 2 / 2.5）都走这里（按实例绑定 models）。
type OpenAIProviderConfig struct {
	APIKey  string `yaml:"api_key"`
	BaseURL string `yaml:"base_url"`
}

type LTXProviderConfig struct {
	BaseURL      string  `yaml:"base_url"`
	Token        string  `yaml:"token"`
	Duration     float64 `yaml:"duration"`
	FPS          int     `yaml:"fps"`
	Seed         int     `yaml:"seed"`
	PollInterval float64 `yaml:"poll_interval"`
}

// DashScopeVideoProviderConfig 承接 Wan/HappyHorse/Kling 图生视频与 wan2.7-videoedit。
type DashScopeVideoProviderConfig struct {
	APIKey       string  `yaml:"api_key"`
	BaseURL      string  `yaml:"base_url"`
	PollInterval float64 `yaml:"poll_interval"`
}

// OminilinkVideoProviderConfig 承接 vg-api.aig-ai.com 异步视频生成。
type OminilinkVideoProviderConfig struct {
	APIKey       string  `yaml:"api_key"`
	BaseURL      string  `yaml:"base_url"`
	PollInterval float64 `yaml:"poll_interval"`
}

// GeminiVideoProviderConfig 承接 Gemini Interactions 图生视频与编辑；auth_header 支持 OminiLink 网关。
type GeminiVideoProviderConfig struct {
	APIKey       string  `yaml:"api_key"`
	BaseURL      string  `yaml:"base_url"`
	ProxyURL     string  `yaml:"proxy_url"`
	AuthHeader   string  `yaml:"auth_header"`
	PollInterval float64 `yaml:"poll_interval"`
}

// ArkVideoProviderConfig 承接方舟 contents/generations 视频任务；当前只绑 Seedance 2.5 文生/首帧。
type ArkVideoProviderConfig struct {
	APIKey       string  `yaml:"api_key"`
	BaseURL      string  `yaml:"base_url"`
	PollInterval float64 `yaml:"poll_interval"`
}

// MinimaxTTSProviderConfig 对齐线上 wgHub Minimax WebSocket TTS。
// 第一版输出格式在代码内固定为 MP3/16kHz/mono，不在此暴露可切换 format，避免调用方与解码链分叉。
type MinimaxTTSProviderConfig struct {
	APIKey        string  `yaml:"api_key"`
	Endpoint      string  `yaml:"endpoint"`
	LanguageBoost string  `yaml:"language_boost"`
	VoiceID       string  `yaml:"voice_id"`
	Speed         float64 `yaml:"speed"`
	Volume        float64 `yaml:"volume"`
	Pitch         int     `yaml:"pitch"`
}

// ElevenLabsTTSProviderConfig 对齐 ElevenLabs HTTP TTS。
// voice_id 必须由部署配置注入（路演可选音色），禁止业务代码写死成唯一路径。
// Stability / SimilarityBoost / Style / Speed 对应控制台滑条；nil 表示不覆盖音色账号默认值。
type ElevenLabsTTSProviderConfig struct {
	APIKey          string   `yaml:"api_key"`
	BaseURL         string   `yaml:"base_url"`
	VoiceID         string   `yaml:"voice_id"`
	Stability       *float64 `yaml:"stability"`
	SimilarityBoost *float64 `yaml:"similarity_boost"`
	Style           *float64 `yaml:"style"`
	Speed           *float64 `yaml:"speed"`
}

// PhotoroomProviderConfig 承接官方 Remove Background Basic plan。
// base_url 可选，默认 https://sdk.photoroom.com；路径固定 /v1/segment，调用方不得注入供应商地址。
// proxy_url 与 Gemini 同语义：未配置时保持原有默认 HTTP transport 行为；CN 等无法直达
// sdk.photoroom.com 的环境须显式配置出口代理（telemetry.NewHTTPClient 使用 DefaultTransport，仍可能读代理环境变量）。
// proxy_url / base_url / api_key 属启动期连接资源，变更须滚动重启，不能热更新部分生效。
type PhotoroomProviderConfig struct {
	APIKey   string `yaml:"api_key"`
	BaseURL  string `yaml:"base_url"`
	ProxyURL string `yaml:"proxy_url"`
}

// SegmentPersonProviderConfig 承接国内自建抠图。
// base_url / method / Basic Auth 只在 ModelHub；人物与商品主体靠 models 里的两个真实 ID 区分。
type SegmentPersonProviderConfig struct {
	BaseURL  string `yaml:"base_url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Method   string `yaml:"method"`
}

// VWorldImageProviderConfig 承接薇光点亮公网生图入口。
// 密码只来自受保护配置/环境变量；不要写内网 10.200.* 或本机 127.0.0.1 调试口。
type VWorldImageProviderConfig struct {
	BaseURL  string `yaml:"base_url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// HumanYOLOProviderConfig 是自建检测服务的地址和 Basic Auth。阈值在供应商代码里，不进 YAML。
type HumanYOLOProviderConfig struct {
	BaseURL  string `yaml:"base_url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// HumanParserProviderConfig 是人体解析地址。账号密码成对，两个都空则匿名。
// G7 只靠网络 ACL，路演应把账号密码留空；不要把 G7 地址写进国内或新加坡配置。
type HumanParserProviderConfig struct {
	BaseURL  string `yaml:"base_url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// RekognitionDetectProviderConfig 是 DetectLabels 的区域和可选静态钥。空钥走任务角色。
type RekognitionDetectProviderConfig struct {
	Region          string `yaml:"region"`
	AccessKeyID     string `yaml:"access_key_id"`
	AccessKeySecret string `yaml:"access_key_secret"`
	SessionToken    string `yaml:"session_token"`
}

// FacebodyCompareProviderConfig 是 CompareFace 的上海端点和成对密钥。比对不走任务角色。
type FacebodyCompareProviderConfig struct {
	Endpoint        string `yaml:"endpoint"`
	AccessKeyID     string `yaml:"access_key_id"`
	AccessKeySecret string `yaml:"access_key_secret"`
}

// RekognitionCompareProviderConfig 是 CompareFaces 的区域和可选静态钥。空钥走任务角色。
type RekognitionCompareProviderConfig struct {
	Region          string `yaml:"region"`
	AccessKeyID     string `yaml:"access_key_id"`
	AccessKeySecret string `yaml:"access_key_secret"`
	SessionToken    string `yaml:"session_token"`
}

type DashScopeEmbeddingProviderConfig struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
}

type CohereEmbeddingProviderConfig struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
}

type BedrockEmbeddingProviderConfig struct {
	Region  string `yaml:"region"`
	RoleARN string `yaml:"role_arn"`
}

type DashScopeRerankProviderConfig struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
}

type BedrockRerankProviderConfig struct {
	Region  string `yaml:"region"`
	RoleARN string `yaml:"role_arn"`
}

type CohereRerankProviderConfig struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
}

type MixedbreadRerankProviderConfig struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
}

type FacebodyDetectProviderConfig struct {
	Endpoint        string `yaml:"endpoint"`
	AccessKeyID     string `yaml:"access_key_id"`
	AccessKeySecret string `yaml:"access_key_secret"`
}

type FacebodyLibraryProviderConfig struct {
	Endpoint        string `yaml:"endpoint"`
	AccessKeyID     string `yaml:"access_key_id"`
	AccessKeySecret string `yaml:"access_key_secret"`
	Database        string `yaml:"database"`
}

type RekognitionFacesProviderConfig struct {
	Region          string `yaml:"region"`
	AccessKeyID     string `yaml:"access_key_id"`
	AccessKeySecret string `yaml:"access_key_secret"`
	SessionToken    string `yaml:"session_token"`
}

type RekognitionLibraryProviderConfig struct {
	Region          string `yaml:"region"`
	AccessKeyID     string `yaml:"access_key_id"`
	AccessKeySecret string `yaml:"access_key_secret"`
	SessionToken    string `yaml:"session_token"`
	Collection      string `yaml:"collection"`
}

type Config struct {
	Server struct {
		ListenAddress       string
		PublicListenAddress string
		// HTTPListenAddress 仅承载 /healthz 与 /metrics；拓扑端口由 WG_SERVER_HTTP_PORT 注入。
		HTTPListenAddress string
	} `yaml:"-"`
	Providers map[string]ProviderConfig `yaml:"providers"`
	// ModelRouteOverrides：真实模型 ID -> 显式选中的 provider 实例名；与各实例 Models 同属路由元数据，可经 ListenConfig 热更新。
	ModelRouteOverrides map[string]string `yaml:"model_routes"`
	// Database 仅服务视频长任务跨 Pod 查询；启动不做 DDL，migration 需显式执行。
	Database DatabaseConfig `yaml:"database"`
	// Logfire 与 Hub 等同项目；token 写在本服务 Nacos，禁止再挂 wg-hub-env。
	Logfire LogfireConfig `yaml:"logfire"`
	// ObjectStorage 保存账本里的图片和视频。留空则只记占位 URI，不挡调用。
	ObjectStorage ObjectStorageConfig `yaml:"object_storage"`
}

// DatabaseConfig 只接受 DSN；连接参数由 DSN 自身表达，避免散落多字段半配置。
type DatabaseConfig struct {
	DSN string `yaml:"dsn"`
}

// LogfireConfig 是跨服务 Trace 导出到同一 Logfire 项目的凭据与身份。
type LogfireConfig struct {
	Token        string `yaml:"token"`
	Env          string `yaml:"env"`
	Service      string `yaml:"service"`
	Version      string `yaml:"version"`
	OtelLogLevel string `yaml:"otel_log_level"`
}

// ObjectStorageConfig 是账本媒体使用的对象存储。字段全空表示不启用。
type ObjectStorageConfig struct {
	// Provider 明确存储厂商，OSS 与 S3 的签名协议不能混用。
	Provider        string `yaml:"provider"`
	Bucket          string `yaml:"bucket"`
	Region          string `yaml:"region"`
	Endpoint        string `yaml:"endpoint"`
	AccessKeyID     string `yaml:"access_key_id"`
	AccessKeySecret string `yaml:"access_key_secret"`
}

func (c ObjectStorageConfig) Enabled() bool {
	return strings.TrimSpace(c.Provider) != "" || strings.TrimSpace(c.Bucket) != "" || strings.TrimSpace(c.Region) != "" || strings.TrimSpace(c.Endpoint) != "" || strings.TrimSpace(c.AccessKeyID) != "" || strings.TrimSpace(c.AccessKeySecret) != ""
}

func (c ObjectStorageConfig) validate() error {
	if !c.Enabled() {
		return nil
	}
	if c.Provider != "oss" && c.Provider != "s3" {
		return fmt.Errorf("object_storage.provider must be oss or s3")
	}
	if strings.TrimSpace(c.Bucket) == "" || strings.TrimSpace(c.Region) == "" {
		return fmt.Errorf("object_storage requires bucket and region")
	}
	if (c.AccessKeyID == "") != (c.AccessKeySecret == "") || (c.Provider == "oss" && (c.AccessKeyID == "" || c.Endpoint == "")) {
		return fmt.Errorf("object_storage requires paired credentials; oss also requires endpoint and static credentials")
	}
	return nil
}

func LoadBootstrapFile(path string) (Bootstrap, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Bootstrap{}, fmt.Errorf("read Nacos bootstrap: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var bootstrap Bootstrap
	if err := decoder.Decode(&bootstrap); err != nil {
		return Bootstrap{}, fmt.Errorf("decode Nacos bootstrap: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Bootstrap{}, fmt.Errorf("Nacos bootstrap contains trailing content")
	}
	bootstrap.ServerAddress = strings.TrimSpace(bootstrap.ServerAddress)
	bootstrap.NamespaceID = strings.TrimSpace(bootstrap.NamespaceID)
	if err := bootstrap.Validate(); err != nil {
		return Bootstrap{}, err
	}
	return bootstrap, nil
}

func (b Bootstrap) Validate() error {
	if b.ServerAddress == "" {
		return fmt.Errorf("Nacos server_address is required")
	}
	_, portText, err := net.SplitHostPort(b.ServerAddress)
	if err != nil {
		return fmt.Errorf("Nacos server_address must be host:port: %w", err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return fmt.Errorf("Nacos server_address port is invalid")
	}
	return nil
}

func newNacosClientConfig(namespaceID string) *constant.ClientConfig {
	options := []constant.ClientOption{
		constant.WithTimeoutMs(10_000),
		constant.WithNotLoadCacheAtStart(true),
		constant.WithAppName("wg-model-hub"),
		constant.WithLogLevel("warn"),
		// Pod 使用非 root 用户，Nacos SDK 的日志和缓存只能写入临时目录。
		constant.WithLogDir("/tmp/wg-model-hub/nacos/log"),
		constant.WithCacheDir("/tmp/wg-model-hub/nacos/cache"),
		constant.WithRamConfig(&constant.RamConfig{}),
	}
	if namespaceID = strings.TrimSpace(namespaceID); namespaceID != "" {
		options = append(options, constant.WithNamespaceId(namespaceID))
	}
	return constant.NewClientConfig(options...)
}

type configGetter interface {
	GetConfig(vo.ConfigParam) (string, error)
	ListenConfig(vo.ConfigParam) error
	CancelListenConfig(vo.ConfigParam) error
}

// NacosConfigLoader 只读取固定 Data ID，并在建立供应商客户端前完成校验（忽略未知字段，拒绝多文档与缺失项）。
type NacosConfigLoader struct {
	client configGetter
	close  func()
}

func NewNacosConfigLoader(bootstrap Bootstrap) (*NacosConfigLoader, error) {
	if err := bootstrap.Validate(); err != nil {
		return nil, err
	}
	host, portText, _ := net.SplitHostPort(bootstrap.ServerAddress)
	port, _ := strconv.ParseUint(portText, 10, 16)
	clientConfig := *newNacosClientConfig(bootstrap.NamespaceID)
	client, err := clients.NewConfigClient(vo.NacosClientParam{
		ClientConfig:  &clientConfig,
		ServerConfigs: []constant.ServerConfig{*constant.NewServerConfig(host, port)},
	})
	if err != nil {
		return nil, fmt.Errorf("create Nacos client: %w", err)
	}
	return &NacosConfigLoader{client: client, close: client.CloseClient}, nil
}

func (l *NacosConfigLoader) Load(ctx context.Context) (Config, string, error) {
	if err := ctx.Err(); err != nil {
		return Config{}, "", err
	}
	content, err := l.client.GetConfig(vo.ConfigParam{DataId: NacosDataID, Group: NacosGroup})
	if err != nil {
		return Config{}, "", fmt.Errorf("read Nacos config: %w", err)
	}
	cfg, err := ParseAndValidateYAML(content)
	if err != nil {
		return Config{}, "", err
	}
	return cfg, content, nil
}

// Listen 贯穿进程生命周期；断线恢复交给 SDK。
func (l *NacosConfigLoader) Listen(onChange func(dataID, group, content string)) error {
	if l == nil || l.client == nil {
		return fmt.Errorf("Nacos config client is not initialized")
	}
	return l.client.ListenConfig(vo.ConfigParam{
		DataId: NacosDataID,
		Group:  NacosGroup,
		OnChange: func(_, group, dataID, data string) {
			if onChange != nil {
				onChange(dataID, group, data)
			}
		},
	})
}

// StopListen 取消当前 Data ID 的监听。
func (l *NacosConfigLoader) StopListen() {
	if l == nil || l.client == nil {
		return
	}
	_ = l.client.CancelListenConfig(vo.ConfigParam{DataId: NacosDataID, Group: NacosGroup})
}

func (l *NacosConfigLoader) Close() {
	if l == nil {
		return
	}
	l.StopListen()
	if l.close != nil {
		l.close()
		l.close = nil
	}
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Logfire.Token) == "" {
		return fmt.Errorf("logfire.token is required")
	}
	if strings.TrimSpace(c.Database.DSN) == "" {
		return fmt.Errorf("database.dsn is required")
	}
	if err := c.ObjectStorage.validate(); err != nil {
		return err
	}
	if len(c.Providers) == 0 {
		return fmt.Errorf("providers are required")
	}

	// 先校验每个实例，再按真实模型 ID 汇总声明方；单声明可隐式路由，多声明必须靠 model_routes 显式选定。
	declaredBy := make(map[string][]string)
	for name, provider := range c.Providers {
		if err := validateProvider(name, provider); err != nil {
			return err
		}
		if len(provider.Models) == 0 {
			return fmt.Errorf("provider %s models are required", name)
		}
		seenInProvider := make(map[string]struct{})
		for _, model := range provider.Models {
			model = strings.TrimSpace(model)
			if model == "" {
				return fmt.Errorf("provider %s contains an empty model id", name)
			}
			if _, dup := seenInProvider[model]; dup {
				return fmt.Errorf("provider %s declares model %s more than once", name, model)
			}
			seenInProvider[model] = struct{}{}
			declaredBy[model] = append(declaredBy[model], name)
		}
	}

	explicit := make(map[string]string, len(c.ModelRouteOverrides))
	for model, providerName := range c.ModelRouteOverrides {
		model = strings.TrimSpace(model)
		providerName = strings.TrimSpace(providerName)
		if model == "" {
			return fmt.Errorf("model_routes contains an empty model id")
		}
		if providerName == "" {
			return fmt.Errorf("model_routes[%s] provider is required", model)
		}
		providers, ok := declaredBy[model]
		if !ok {
			return fmt.Errorf("model_routes[%s] is not declared by any provider", model)
		}
		if _, exists := c.Providers[providerName]; !exists {
			return fmt.Errorf("model_routes[%s] references unknown provider %s", model, providerName)
		}
		found := false
		for _, declared := range providers {
			if declared == providerName {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("model_routes[%s] provider %s does not declare that model", model, providerName)
		}
		explicit[model] = providerName
	}

	for model, providers := range declaredBy {
		if len(providers) == 1 {
			continue
		}
		if _, ok := explicit[model]; !ok {
			sorted := append([]string(nil), providers...)
			sort.Strings(sorted)
			return fmt.Errorf("model %s is declared by multiple providers %s; set model_routes[%s]", model, strings.Join(sorted, ", "), model)
		}
	}
	return nil
}

// ApplyListenPortOverridesFromEnv 在 server 启动边界装配内外网 gRPC 与内部 HTTP 监听地址；Pod 拓扑端口不得由 Nacos 业务正文拥有。
func ApplyListenPortOverridesFromEnv(cfg *Config) error {
	raw := strings.TrimSpace(os.Getenv("WG_SERVER_GRPC_PORT"))
	if raw == "" {
		return fmt.Errorf("missing WG_SERVER_GRPC_PORT: server startup requires Deployment-injected listen port")
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port <= 0 || port > 65535 {
		return fmt.Errorf("WG_SERVER_GRPC_PORT must be a valid listen port")
	}
	cfg.Server.ListenAddress = fmt.Sprintf(":%d", port)

	httpRaw := strings.TrimSpace(os.Getenv("WG_SERVER_HTTP_PORT"))
	if httpRaw == "" {
		return fmt.Errorf("missing WG_SERVER_HTTP_PORT: server startup requires Deployment-injected metrics listen port")
	}
	httpPort, err := strconv.Atoi(httpRaw)
	if err != nil || httpPort <= 0 || httpPort > 65535 {
		return fmt.Errorf("WG_SERVER_HTTP_PORT must be a valid listen port")
	}
	cfg.Server.HTTPListenAddress = fmt.Sprintf(":%d", httpPort)

	publicRaw := strings.TrimSpace(os.Getenv("WG_SERVER_PUBLIC_GRPC_PORT"))
	if publicRaw == "" {
		// 未显式开启公网 listener 时保持关闭，避免误暴露内网-only 部署。
		cfg.Server.PublicListenAddress = ""
		return nil
	}
	publicPort, err := strconv.Atoi(publicRaw)
	if err != nil || publicPort <= 0 || publicPort > 65535 {
		return fmt.Errorf("WG_SERVER_PUBLIC_GRPC_PORT must be a valid listen port when set")
	}
	cfg.Server.PublicListenAddress = fmt.Sprintf(":%d", publicPort)
	return nil
}

// ModelRoutes 返回真实模型 ID -> provider 实例名；单声明隐式选定，多声明取 model_routes。
func (c Config) ModelRoutes() map[string]string {
	overrides := make(map[string]string, len(c.ModelRouteOverrides))
	for model, providerName := range c.ModelRouteOverrides {
		overrides[strings.TrimSpace(model)] = strings.TrimSpace(providerName)
	}
	declaredBy := make(map[string][]string)
	for name, provider := range c.Providers {
		// ASR 示例允许保留空凭据；无凭据实例不进入真实路由，也不会被 ListModels 暴露。
		if IsASRProvider(provider) && !ASRCredentialsPresent(provider) {
			continue
		}
		for _, model := range provider.Models {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			declaredBy[model] = append(declaredBy[model], name)
		}
	}
	routes := make(map[string]string, len(declaredBy))
	for model, providers := range declaredBy {
		if selected, ok := overrides[model]; ok && selected != "" {
			routes[model] = selected
			continue
		}
		if len(providers) == 1 {
			routes[model] = providers[0]
		}
	}
	return routes
}

func validateProvider(name string, provider ProviderConfig) error {
	switch countConcreteProviders(provider) {
	case 0:
		return fmt.Errorf("provider %s must set exactly one concrete provider type", name)
	case 1:
	default:
		return fmt.Errorf("provider %s must set exactly one concrete provider type", name)
	}
	switch {
	case provider.Gemini != nil:
		if strings.TrimSpace(provider.Gemini.APIKey) == "" {
			return fmt.Errorf("provider %s api_key is required", name)
		}
	case provider.VertexAI != nil:
		if strings.TrimSpace(provider.VertexAI.APIKey) == "" {
			return fmt.Errorf("provider %s api_key is required", name)
		}
		// project 可选。只配 api_key 的文本 Express 仍合法。
		// 不按 models 是否含 Nano Banana 2.1 强制 project：模型列表可热添加，客户端在启动时已经建好。
		project := provider.VertexAI.Project
		if project != "" && (strings.TrimSpace(project) != project || strings.ContainsAny(project, "/ \t")) {
			return fmt.Errorf("provider %s vertexai.project is invalid", name)
		}
	case provider.Ark != nil:
		if strings.TrimSpace(provider.Ark.APIKey) == "" {
			return fmt.Errorf("provider %s api_key is required", name)
		}
		// endpoint_id 绑定单一推理部署；多模型共用同一 endpoint 会在路由层产生歧义，启动时拒绝。
		if strings.TrimSpace(provider.Ark.EndpointID) != "" && len(provider.Models) != 1 {
			return fmt.Errorf("provider %s endpoint_id requires exactly one model", name)
		}
	case provider.OpenAI != nil:
		if strings.TrimSpace(provider.OpenAI.APIKey) == "" {
			return fmt.Errorf("provider %s api_key is required", name)
		}
	case provider.ImageSeg != nil:
		cfg := provider.ImageSeg
		if cfg.Endpoint == "" || cfg.AccessKeyID == "" || cfg.AccessKeySecret == "" {
			return fmt.Errorf("provider %s image_seg endpoint and key pair required", name)
		}
	case provider.Fashion != nil:
		if strings.TrimSpace(provider.Fashion.BaseURL) == "" {
			return fmt.Errorf("provider %s fashion base_url is required", name)
		}
	case provider.LTX != nil:
		ltx := provider.LTX
		if strings.TrimSpace(ltx.BaseURL) == "" ||
			ltx.Duration <= 0 ||
			ltx.FPS <= 0 ||
			ltx.PollInterval <= 0 {
			return fmt.Errorf("provider %s LTX configuration is incomplete", name)
		}
	case provider.DashScopeVideo != nil:
		if strings.TrimSpace(provider.DashScopeVideo.APIKey) == "" {
			return fmt.Errorf("provider %s api_key is required", name)
		}
	case provider.OminilinkVideo != nil:
		if strings.TrimSpace(provider.OminilinkVideo.APIKey) == "" {
			return fmt.Errorf("provider %s api_key is required", name)
		}
	case provider.GeminiVideo != nil:
		if strings.TrimSpace(provider.GeminiVideo.APIKey) == "" {
			return fmt.Errorf("provider %s api_key is required", name)
		}
	case provider.ArkVideo != nil:
		if strings.TrimSpace(provider.ArkVideo.APIKey) == "" {
			return fmt.Errorf("provider %s api_key is required", name)
		}
	case provider.MinimaxTTS != nil:
		if strings.TrimSpace(provider.MinimaxTTS.APIKey) == "" {
			return fmt.Errorf("provider %s api_key is required", name)
		}
	case provider.ElevenLabsTTS != nil:
		if strings.TrimSpace(provider.ElevenLabsTTS.APIKey) == "" {
			return fmt.Errorf("provider %s api_key is required", name)
		}
		if strings.TrimSpace(provider.ElevenLabsTTS.VoiceID) == "" {
			return fmt.Errorf("provider %s voice_id is required", name)
		}
		// 路演可按控制台比例调口播；越界直接拒配，避免静默夹逼成听感漂移。
		if err := validateElevenLabsVoiceSettings(name, provider.ElevenLabsTTS); err != nil {
			return err
		}
	case IsASRProvider(provider):
		// 凭据全部缺失表示该环境未启用此厂家，启动时跳过；只填一半则拒绝，避免生成坏路由。
		if !ASRCredentialsPresent(provider) {
			return nil
		}
		if provider.VolcengineASR != nil {
			v := provider.VolcengineASR
			if strings.TrimSpace(v.APIKey) == "" && (strings.TrimSpace(v.AppKey) == "" || strings.TrimSpace(v.AccessKey) == "") {
				return fmt.Errorf("provider %s volcengine_asr requires api_key or app_key/access_key", name)
			}
		}
		if provider.AliyunASR != nil {
			v := provider.AliyunASR
			if strings.TrimSpace(v.AppKey) == "" || (strings.TrimSpace(v.Token) == "" && (strings.TrimSpace(v.AKID) == "" || strings.TrimSpace(v.AKKey) == "")) {
				return fmt.Errorf("provider %s aliyun_asr requires app_key and token or AK pair", name)
			}
		}
		if provider.TencentASR != nil {
			v := provider.TencentASR
			if strings.TrimSpace(v.AppID) == "" || strings.TrimSpace(v.SecretID) == "" || strings.TrimSpace(v.SecretKey) == "" {
				return fmt.Errorf("provider %s tencent_asr requires app_id, secret_id and secret_key", name)
			}
		}
	case provider.Photoroom != nil:
		if strings.TrimSpace(provider.Photoroom.APIKey) == "" {
			return fmt.Errorf("provider %s api_key is required", name)
		}
	case provider.SegmentPerson != nil:
		segment := provider.SegmentPerson
		if strings.TrimSpace(segment.BaseURL) == "" || strings.TrimSpace(segment.Method) == "" {
			return fmt.Errorf("provider %s segment_person base_url and method are required", name)
		}
		if (strings.TrimSpace(segment.Username) == "") != (strings.TrimSpace(segment.Password) == "") {
			return fmt.Errorf("provider %s segment_person username and password must be configured together", name)
		}
	case provider.VWorldImage != nil:
		vworld := provider.VWorldImage
		if strings.TrimSpace(vworld.BaseURL) == "" || strings.TrimSpace(vworld.Username) == "" || strings.TrimSpace(vworld.Password) == "" {
			return fmt.Errorf("provider %s vworld_image base_url, username and password are required", name)
		}
	case provider.HumanYOLO != nil:
		yolo := provider.HumanYOLO
		if strings.TrimSpace(yolo.BaseURL) == "" || strings.TrimSpace(yolo.Username) == "" || strings.TrimSpace(yolo.Password) == "" {
			return fmt.Errorf("provider %s human_yolo base_url, username and password are required", name)
		}
	case provider.HumanParser != nil:
		parser := provider.HumanParser
		if strings.TrimSpace(parser.BaseURL) == "" {
			return fmt.Errorf("provider %s human_parser base_url is required", name)
		}
		if (strings.TrimSpace(parser.Username) == "") != (strings.TrimSpace(parser.Password) == "") {
			return fmt.Errorf("provider %s human_parser username and password must be configured together", name)
		}
	case provider.RekognitionDetect != nil:
		detect := provider.RekognitionDetect
		region := strings.TrimSpace(detect.Region)
		if region == "" || strings.HasPrefix(strings.ToLower(region), "cn-") {
			return fmt.Errorf("provider %s rekognition region is required and cannot be cn-*", name)
		}
		key, secret := strings.TrimSpace(detect.AccessKeyID), strings.TrimSpace(detect.AccessKeySecret)
		if (key == "") != (secret == "") || (strings.TrimSpace(detect.SessionToken) != "" && key == "") {
			return fmt.Errorf("provider %s rekognition access key and secret must be configured together", name)
		}
	case provider.FacebodyCompare != nil:
		compare := provider.FacebodyCompare
		if strings.TrimSpace(compare.Endpoint) == "" || strings.TrimSpace(compare.AccessKeyID) == "" || strings.TrimSpace(compare.AccessKeySecret) == "" {
			return fmt.Errorf("provider %s facebody endpoint, access_key_id and access_key_secret are required", name)
		}
	case provider.RekognitionCompare != nil:
		compare := provider.RekognitionCompare
		region := strings.TrimSpace(compare.Region)
		if region == "" || strings.HasPrefix(strings.ToLower(region), "cn-") {
			return fmt.Errorf("provider %s rekognition region is required and cannot be cn-*", name)
		}
		key, secret := strings.TrimSpace(compare.AccessKeyID), strings.TrimSpace(compare.AccessKeySecret)
		if (key == "") != (secret == "") || (strings.TrimSpace(compare.SessionToken) != "" && key == "") {
			return fmt.Errorf("provider %s rekognition access key and secret must be configured together", name)
		}
	case provider.DashScopeEmbedding != nil:
		item := provider.DashScopeEmbedding
		if strings.TrimSpace(item.BaseURL) == "" || strings.TrimSpace(item.APIKey) == "" {
			return fmt.Errorf("provider %s dashscope embedding base_url and api_key are required", name)
		}
	case provider.CohereEmbedding != nil:
		item := provider.CohereEmbedding
		if strings.TrimSpace(item.BaseURL) == "" || strings.TrimSpace(item.APIKey) == "" {
			return fmt.Errorf("provider %s cohere embedding base_url and api_key are required", name)
		}
	case provider.BedrockEmbedding != nil:
		if strings.TrimSpace(provider.BedrockEmbedding.Region) == "" || strings.HasPrefix(strings.ToLower(strings.TrimSpace(provider.BedrockEmbedding.Region)), "cn-") {
			return fmt.Errorf("provider %s bedrock embedding region is required and cannot be cn-*", name)
		}
	case provider.DashScopeRerank != nil:
		item := provider.DashScopeRerank
		if strings.TrimSpace(item.BaseURL) == "" || strings.TrimSpace(item.APIKey) == "" {
			return fmt.Errorf("provider %s dashscope rerank base_url and api_key are required", name)
		}
	case provider.BedrockRerank != nil:
		if strings.TrimSpace(provider.BedrockRerank.Region) == "" || strings.HasPrefix(strings.ToLower(strings.TrimSpace(provider.BedrockRerank.Region)), "cn-") {
			return fmt.Errorf("provider %s bedrock rerank region is required and cannot be cn-*", name)
		}
	case provider.CohereRerank != nil:
		item := provider.CohereRerank
		if strings.TrimSpace(item.BaseURL) == "" || strings.TrimSpace(item.APIKey) == "" {
			return fmt.Errorf("provider %s cohere rerank base_url and api_key are required", name)
		}
	case provider.MixedbreadRerank != nil:
		item := provider.MixedbreadRerank
		if strings.TrimSpace(item.BaseURL) == "" || strings.TrimSpace(item.APIKey) == "" {
			return fmt.Errorf("provider %s mixedbread rerank base_url and api_key are required", name)
		}
	case provider.FacebodyDetect != nil:
		item := provider.FacebodyDetect
		if strings.TrimSpace(item.Endpoint) == "" || strings.TrimSpace(item.AccessKeyID) == "" || strings.TrimSpace(item.AccessKeySecret) == "" {
			return fmt.Errorf("provider %s facebody detect endpoint and key pair are required", name)
		}
	case provider.FacebodyLibrary != nil:
		item := provider.FacebodyLibrary
		if strings.TrimSpace(item.Endpoint) == "" || strings.TrimSpace(item.AccessKeyID) == "" || strings.TrimSpace(item.AccessKeySecret) == "" || strings.TrimSpace(item.Database) == "" {
			return fmt.Errorf("provider %s facebody library endpoint, key pair and database are required", name)
		}
	case provider.RekognitionFaces != nil:
		if err := validateRekognitionRegion(name, provider.RekognitionFaces.Region, provider.RekognitionFaces.AccessKeyID, provider.RekognitionFaces.AccessKeySecret, provider.RekognitionFaces.SessionToken); err != nil {
			return err
		}
	case provider.RekognitionLibrary != nil:
		if err := validateRekognitionRegion(name, provider.RekognitionLibrary.Region, provider.RekognitionLibrary.AccessKeyID, provider.RekognitionLibrary.AccessKeySecret, provider.RekognitionLibrary.SessionToken); err != nil {
			return err
		}
		if strings.TrimSpace(provider.RekognitionLibrary.Collection) == "" {
			return fmt.Errorf("provider %s rekognition library collection is required", name)
		}
	}
	return nil
}

func countConcreteProviders(provider ProviderConfig) int {
	n := 0
	if provider.ImageSeg != nil {
		n++
	}
	if provider.Fashion != nil {
		n++
	}
	if provider.Gemini != nil {
		n++
	}
	if provider.VertexAI != nil {
		n++
	}
	if provider.Ark != nil {
		n++
	}
	if provider.OpenAI != nil {
		n++
	}
	if provider.LTX != nil {
		n++
	}
	if provider.DashScopeVideo != nil {
		n++
	}
	if provider.OminilinkVideo != nil {
		n++
	}
	if provider.GeminiVideo != nil {
		n++
	}
	if provider.ArkVideo != nil {
		n++
	}
	if provider.MinimaxTTS != nil {
		n++
	}
	if provider.ElevenLabsTTS != nil {
		n++
	}
	if provider.VolcengineASR != nil {
		n++
	}
	if provider.AliyunASR != nil {
		n++
	}
	if provider.FunASR != nil {
		n++
	}
	if provider.QwenASR != nil {
		n++
	}
	if provider.TencentASR != nil {
		n++
	}
	if provider.AssemblyAIASR != nil {
		n++
	}
	if provider.DeepgramASR != nil {
		n++
	}
	if provider.SonioxASR != nil {
		n++
	}
	if provider.Photoroom != nil {
		n++
	}
	if provider.SegmentPerson != nil {
		n++
	}
	if provider.VWorldImage != nil {
		n++
	}
	if provider.HumanYOLO != nil {
		n++
	}
	if provider.HumanParser != nil {
		n++
	}
	if provider.RekognitionDetect != nil {
		n++
	}
	if provider.FacebodyCompare != nil {
		n++
	}
	if provider.RekognitionCompare != nil {
		n++
	}
	if provider.DashScopeEmbedding != nil {
		n++
	}
	if provider.CohereEmbedding != nil {
		n++
	}
	if provider.BedrockEmbedding != nil {
		n++
	}
	if provider.DashScopeRerank != nil {
		n++
	}
	if provider.BedrockRerank != nil {
		n++
	}
	if provider.CohereRerank != nil {
		n++
	}
	if provider.MixedbreadRerank != nil {
		n++
	}
	if provider.FacebodyDetect != nil {
		n++
	}
	if provider.FacebodyLibrary != nil {
		n++
	}
	if provider.RekognitionFaces != nil {
		n++
	}
	if provider.RekognitionLibrary != nil {
		n++
	}
	return n
}

func validateRekognitionRegion(name, region, accessKey, secret, sessionToken string) error {
	region = strings.TrimSpace(region)
	if region == "" || strings.HasPrefix(strings.ToLower(region), "cn-") {
		return fmt.Errorf("provider %s rekognition region is required and cannot be cn-*", name)
	}
	key, secretKey := strings.TrimSpace(accessKey), strings.TrimSpace(secret)
	if (key == "") != (secretKey == "") || (strings.TrimSpace(sessionToken) != "" && key == "") {
		return fmt.Errorf("provider %s rekognition access key and secret must be configured together", name)
	}
	return nil
}

// validateElevenLabsVoiceSettings 校验可选口播滑条；未配置保持供应商音色默认，不强制写死。
func validateElevenLabsVoiceSettings(name string, cfg *ElevenLabsTTSProviderConfig) error {
	if cfg == nil {
		return nil
	}
	check01 := func(field string, v *float64) error {
		if v == nil {
			return nil
		}
		if *v < 0 || *v > 1 {
			return fmt.Errorf("provider %s elevenlabs_tts.%s must be between 0 and 1", name, field)
		}
		return nil
	}
	if err := check01("stability", cfg.Stability); err != nil {
		return err
	}
	if err := check01("similarity_boost", cfg.SimilarityBoost); err != nil {
		return err
	}
	if err := check01("style", cfg.Style); err != nil {
		return err
	}
	if cfg.Speed != nil && (*cfg.Speed < 0.25 || *cfg.Speed > 4) {
		return fmt.Errorf("provider %s elevenlabs_tts.speed must be between 0.25 and 4", name)
	}
	return nil
}

// ProviderSupports 根据供应商类型判断能力；ASR/Speech 是专用 RPC，不经过 Generate OutputSpec。
func ProviderSupports(provider ProviderConfig, capability string) bool {
	switch {
	case provider.Gemini != nil, provider.OpenAI != nil:
		return capability == CapabilityText || capability == CapabilityImage
	case provider.ImageSeg != nil, provider.Photoroom != nil, provider.SegmentPerson != nil, provider.VWorldImage != nil:
		return capability == CapabilityImage
	case provider.Fashion != nil, provider.HumanYOLO != nil, provider.HumanParser != nil, provider.RekognitionDetect != nil, provider.FacebodyCompare != nil, provider.RekognitionCompare != nil, provider.DashScopeEmbedding != nil, provider.CohereEmbedding != nil, provider.BedrockEmbedding != nil, provider.DashScopeRerank != nil, provider.BedrockRerank != nil, provider.CohereRerank != nil, provider.MixedbreadRerank != nil, provider.FacebodyDetect != nil, provider.FacebodyLibrary != nil, provider.RekognitionFaces != nil, provider.RekognitionLibrary != nil:
		return capability == CapabilityText
	case provider.VertexAI != nil:
		// 类型能力不读 models。该列表可热更新，不能用来决定进程里有没有生图客户端。
		return capability == CapabilityText || capability == CapabilityImage
	case provider.Ark != nil:
		return capability == CapabilityText
	case provider.LTX != nil:
		return capability == CapabilityVideo
	case provider.DashScopeVideo != nil, provider.OminilinkVideo != nil, provider.GeminiVideo != nil, provider.ArkVideo != nil:
		return capability == CapabilityVideo
	case provider.MinimaxTTS != nil, provider.ElevenLabsTTS != nil:
		return capability == CapabilitySpeech
	case IsASRProvider(provider):
		return capability == CapabilityASR
	default:
		return false
	}
}

// VertexImageRequestAllowed 只看这次请求的真实模型，不看实例上的 models 列表。
// 同实例热加入生图 ID 后，已有文本模型的图片请求仍然拒绝。
func VertexImageRequestAllowed(provider ProviderConfig, model, capability string) bool {
	if provider.VertexAI == nil || capability != CapabilityImage {
		return true
	}
	category, ok := models.CategoryOf(strings.TrimSpace(model))
	return ok && category == models.CategoryImageGeneration
}

// IsASRProvider 只判断配置类型；凭据是否足够由 ASRCredentialsPresent 单独判断。
func IsASRProvider(provider ProviderConfig) bool {
	return provider.VolcengineASR != nil || provider.AliyunASR != nil || provider.FunASR != nil ||
		provider.QwenASR != nil || provider.TencentASR != nil || provider.AssemblyAIASR != nil ||
		provider.DeepgramASR != nil || provider.SonioxASR != nil
}

// ASRCredentialsPresent 用于环境裁剪：缺密钥的厂家不绑定模型，避免占位配置制造“可用”假象。
func ASRCredentialsPresent(provider ProviderConfig) bool {
	switch {
	case provider.VolcengineASR != nil:
		v := provider.VolcengineASR
		return strings.TrimSpace(v.APIKey) != "" || strings.TrimSpace(v.AppKey) != "" || strings.TrimSpace(v.AccessKey) != ""
	case provider.AliyunASR != nil:
		v := provider.AliyunASR
		return strings.TrimSpace(v.AppKey) != "" || strings.TrimSpace(v.Token) != "" || strings.TrimSpace(v.AKID) != "" || strings.TrimSpace(v.AKKey) != ""
	case provider.FunASR != nil:
		return strings.TrimSpace(provider.FunASR.APIKey) != ""
	case provider.QwenASR != nil:
		return strings.TrimSpace(provider.QwenASR.APIKey) != ""
	case provider.TencentASR != nil:
		v := provider.TencentASR
		return strings.TrimSpace(v.AppID) != "" || strings.TrimSpace(v.SecretID) != "" || strings.TrimSpace(v.SecretKey) != ""
	case provider.AssemblyAIASR != nil:
		return strings.TrimSpace(provider.AssemblyAIASR.APIKey) != ""
	case provider.DeepgramASR != nil:
		return strings.TrimSpace(provider.DeepgramASR.APIKey) != ""
	case provider.SonioxASR != nil:
		return strings.TrimSpace(provider.SonioxASR.APIKey) != ""
	default:
		return false
	}
}
