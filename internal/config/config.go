package config

import (
	"context"
	"fmt"
	"time"

	_ "github.com/joho/godotenv/autoload"
	"github.com/sethvargo/go-envconfig"
	baseconfig "gitlab.services.mts.ru/salsa/go-base/application/config"
)

type Config struct {
	App        baseconfig.AppConfig
	Database   DatabaseConfig  `env:",prefix=DATABASE_"`
	Allure     AllureConfig    `env:",prefix=ALLURE_"`
	LLM        OpenAIConfig    `env:",prefix=LLM_"`
	Embeddings EmbeddingConfig `env:",prefix=EMBEDDINGS_"`
	Analysis   AnalysisConfig  `env:",prefix=ANALYSIS_"`
}

type DatabaseConfig struct {
	URL      string `env:"URL,required"`
	MinConns int32  `env:"MIN_CONNS,default=1"`
	MaxConns int32  `env:"MAX_CONNS,default=8"`
}

type AllureConfig struct {
	BaseURL            string        `env:"BASE_URL,default=https://allure.services.mts.ru"`
	ProjectID          int64         `env:"PROJECT_ID"`
	UserToken          string        `env:"USER_TOKEN"`
	BearerToken        string        `env:"BEARER_TOKEN"`
	InsecureSkipVerify bool          `env:"INSECURE_SKIP_VERIFY,default=false"`
	PageSize           int           `env:"PAGE_SIZE,default=200"`
	SyncLaunchLimit    int           `env:"SYNC_LAUNCH_LIMIT,default=20"`
	Timeout            time.Duration `env:"TIMEOUT,default=30s"`
	LaunchURLTemplate  string        `env:"LAUNCH_URL_TEMPLATE,default={base_url}/launch/{launch_id}"`
	TestURLTemplate    string        `env:"TEST_URL_TEMPLATE,default={base_url}/launch/{launch_id}/testresult/{test_result_id}"`
}

type OpenAIConfig struct {
	Enabled            bool          `env:"ENABLED,default=false"`
	BaseURL            string        `env:"BASE_URL,default=https://api.openai.com/v1"`
	APIKey             string        `env:"API_KEY"`
	Model              string        `env:"MODEL,default=gpt-4.1-mini"`
	InsecureSkipVerify bool          `env:"INSECURE_SKIP_VERIFY,default=false"`
	Timeout            time.Duration `env:"TIMEOUT,default=60s"`
	Temperature        float64       `env:"TEMPERATURE,default=0.1"`
}

type EmbeddingConfig struct {
	Enabled            bool          `env:"ENABLED,default=false"`
	Provider           string        `env:"PROVIDER,default=openai"`
	BaseURL            string        `env:"BASE_URL,default=https://api.openai.com/v1"`
	APIKey             string        `env:"API_KEY"`
	Model              string        `env:"MODEL,default=text-embedding-3-small"`
	InsecureSkipVerify bool          `env:"INSECURE_SKIP_VERIFY,default=false"`
	Timeout            time.Duration `env:"TIMEOUT,default=30s"`
}

type AnalysisConfig struct {
	TopKPerQuery           int     `env:"TOP_K_PER_QUERY,default=8"`
	MaxCandidates          int     `env:"MAX_CANDIDATES,default=12"`
	ScoreThreshold         float64 `env:"SCORE_THRESHOLD,default=0.55"`
	HistoryWindow          int     `env:"HISTORY_WINDOW,default=20"`
	StrongDefectThreshold  float64 `env:"STRONG_DEFECT_THRESHOLD,default=0.78"`
	StrongRerunPassRate    float64 `env:"STRONG_RERUN_PASS_RATE,default=0.70"`
	AttachCandidateMinimum float64 `env:"ATTACH_CANDIDATE_MINIMUM,default=0.70"`
	TransientPatternBoost  float64 `env:"TRANSIENT_PATTERN_BOOST,default=0.10"`
	SemanticSearchEnabled  bool    `env:"SEMANTIC_SEARCH_ENABLED,default=true"`
	SemanticViewName       string  `env:"SEMANTIC_VIEW_NAME,default=knowledge_documents_embedding"`
}

func Load(ctx context.Context) (Config, error) {
	var cfg Config
	if err := envconfig.Process(ctx, &cfg); err != nil {
		return Config{}, fmt.Errorf("load env config: %w", err)
	}
	return cfg, nil
}
