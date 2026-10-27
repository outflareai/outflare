package bad

import (
	"os"

	"github.com/openai/openai-go"
)

func keys(prefix string) []string {
	return []string{
		os.Getenv("ANTHROPIC_API_KEY"),
		os.Getenv("OUTFLARE_GEONODE_PASSWORD"),
		os.Getenv("APOLLO_TOKEN"),
		os.Getenv("SHARED_LLM_KEY"),
		os.Getenv(prefix + "API_KEY"),
		os.Getenv("DATABASE_URL"),
		os.Getenv("OUTFLARE_SECRET_KEY"),
		os.Getenv("PORT"),
		os.Getenv("GOOGLE_OAUTH_CLIENT_ID"),
	}
}
