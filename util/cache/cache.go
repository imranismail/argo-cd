package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/go-redis/redis"
	"github.com/spf13/cobra"

	"github.com/argoproj/argo-cd/common"
	appv1 "github.com/argoproj/argo-cd/pkg/apis/application/v1alpha1"
	"github.com/argoproj/argo-cd/util/hash"
)

const (
	defaultCacheExpiration          = 24 * time.Hour
	connectionStatusCacheExpiration = 1 * time.Hour
	appStateCacheExpiration         = 1 * time.Hour
	repoCacheExpiration             = 24 * time.Hour
	oidcCacheExpiration             = 3 * time.Minute

	// envRedisPassword is a env variable name which stores redis password
	envRedisPassword = "REDIS_PASSWORD"
)

type OIDCState struct {
	// ReturnURL is the URL in which to redirect a user back to after completing an OAuth2 login
	ReturnURL string `json:"returnURL"`
}

// NewCache creates new instance of Cache
func NewCache(cacheClient CacheClient) *Cache {
	return &Cache{client: cacheClient}
}

// AddCacheFlagsToCmd adds flags which control caching to the specified command
func AddCacheFlagsToCmd(cmd *cobra.Command) func() (*Cache, error) {
	redisAddress := ""
	sentinelAddresses := make([]string, 0)
	sentinelMaster := ""
	redisDB := 0

	cmd.Flags().StringVar(&redisAddress, "redis", "", "Redis server hostname and port (e.g. argocd-redis:6379). ")
	cmd.Flags().IntVar(&redisDB, "redisdb", 0, "Redis database.")
	cmd.Flags().StringArrayVar(&sentinelAddresses, "sentinel", []string{}, "Redis sentinel hostname and port (e.g. argocd-redis-ha-announce-0:6379). ")
	cmd.Flags().StringVar(&sentinelMaster, "sentinelmaster", "master", "Redis sentinel master group name.")
	return func() (*Cache, error) {
		password := os.Getenv(envRedisPassword)
		if len(sentinelAddresses) > 0 {
			client := redis.NewFailoverClient(&redis.FailoverOptions{
				MasterName:    sentinelMaster,
				SentinelAddrs: sentinelAddresses,
				DB:            redisDB,
				Password:      password,
			})
			return NewCache(NewRedisCache(client, defaultCacheExpiration)), nil
		}

		if redisAddress == "" {
			redisAddress = common.DefaultRedisAddr
		}
		client := redis.NewClient(&redis.Options{
			Addr:     redisAddress,
			Password: password,
			DB:       redisDB,
		})
		return NewCache(NewRedisCache(client, defaultCacheExpiration)), nil
	}
}

// Cache provides strongly types methods to store and retrieve values from shared cache
type Cache struct {
	client CacheClient
}

func appManagedResourcesKey(appName string) string {
	return fmt.Sprintf("app|managed-resources|%s", appName)
}

func appResourcesTreeKey(appName string) string {
	return fmt.Sprintf("app|resources-tree|%s", appName)
}

func clusterConnectionStateKey(server string) string {
	return fmt.Sprintf("cluster|%s|connection-state", server)
}

func repoConnectionStateKey(repo string) string {
	return fmt.Sprintf("repo|%s|connection-state", repo)
}

func listApps(repoURL, revision string) string {
	return fmt.Sprintf("ldir|%s|%s", repoURL, revision)
}

func oidcStateKey(key string) string {
	return fmt.Sprintf("oidc|%s", key)
}

func appSourceKey(appSrc *appv1.ApplicationSource) uint32 {
	appSrc = appSrc.DeepCopy()
	if !appSrc.IsHelm() {
		appSrc.RepoURL = ""        // superceded by commitSHA
		appSrc.TargetRevision = "" // superceded by commitSHA
	}
	appSrcStr, _ := json.Marshal(appSrc)
	return hash.FNVa(string(appSrcStr))
}

func manifestCacheKey(commitSHA string, appSrc *appv1.ApplicationSource, namespace string, appLabelKey string, appLabelValue string) string {
	return fmt.Sprintf("mfst|%s|%s|%s|%s|%d", appLabelKey, appLabelValue, commitSHA, namespace, appSourceKey(appSrc))
}

func appDetailsCacheKey(commitSHA string, appSrc *appv1.ApplicationSource) string {
	return fmt.Sprintf("appdetails|%s|%d", commitSHA, appSourceKey(appSrc))
}

func revisionMetadataKey(repoURL, revision string) string {
	return fmt.Sprintf("revisionmetadata|%s|%s", repoURL, revision)
}

func (c *Cache) setItem(key string, item interface{}, expiration time.Duration, delete bool) error {
	key = fmt.Sprintf("%s|%s", key, common.CacheVersion)
	if delete {
		return c.client.Delete(key)
	} else {
		return c.client.Set(&Item{Object: item, Key: key, Expiration: expiration})
	}
}

func (c *Cache) getItem(key string, item interface{}) error {
	key = fmt.Sprintf("%s|%s", key, common.CacheVersion)
	return c.client.Get(key, item)
}

func (c *Cache) GetAppManagedResources(appName string, res *[]*appv1.ResourceDiff) error {
	err := c.getItem(appManagedResourcesKey(appName), &res)
	return err
}

func (c *Cache) SetAppManagedResources(appName string, managedResources []*appv1.ResourceDiff) error {
	return c.setItem(appManagedResourcesKey(appName), managedResources, appStateCacheExpiration, managedResources == nil)
}

func (c *Cache) GetAppResourcesTree(appName string, res *appv1.ApplicationTree) error {
	err := c.getItem(appResourcesTreeKey(appName), &res)
	return err
}

func (c *Cache) SetAppResourcesTree(appName string, resourcesTree *appv1.ApplicationTree) error {
	return c.setItem(appResourcesTreeKey(appName), resourcesTree, appStateCacheExpiration, resourcesTree == nil)
}

func (c *Cache) GetClusterConnectionState(server string) (appv1.ConnectionState, error) {
	res := appv1.ConnectionState{}
	err := c.getItem(clusterConnectionStateKey(server), &res)
	return res, err
}

func (c *Cache) SetClusterConnectionState(server string, state *appv1.ConnectionState) error {
	return c.setItem(clusterConnectionStateKey(server), &state, connectionStatusCacheExpiration, state == nil)
}

func (c *Cache) GetRepoConnectionState(repo string) (appv1.ConnectionState, error) {
	res := appv1.ConnectionState{}
	err := c.getItem(repoConnectionStateKey(repo), &res)
	return res, err
}

func (c *Cache) SetRepoConnectionState(repo string, state *appv1.ConnectionState) error {
	return c.setItem(repoConnectionStateKey(repo), &state, connectionStatusCacheExpiration, state == nil)
}
func (c *Cache) ListApps(repoUrl, revision string) (map[string]string, error) {
	res := make(map[string]string)
	err := c.getItem(listApps(repoUrl, revision), &res)
	return res, err
}

func (c *Cache) SetApps(repoUrl, revision string, apps map[string]string) error {
	return c.setItem(listApps(repoUrl, revision), apps, repoCacheExpiration, apps == nil)
}

func (c *Cache) GetManifests(commitSHA string, appSrc *appv1.ApplicationSource, namespace string, appLabelKey string, appLabelValue string, res interface{}) error {
	return c.getItem(manifestCacheKey(commitSHA, appSrc, namespace, appLabelKey, appLabelValue), res)
}

func (c *Cache) SetManifests(commitSHA string, appSrc *appv1.ApplicationSource, namespace string, appLabelKey string, appLabelValue string, res interface{}) error {
	return c.setItem(manifestCacheKey(commitSHA, appSrc, namespace, appLabelKey, appLabelValue), res, repoCacheExpiration, res == nil)
}

func (c *Cache) GetAppDetails(commitSHA string, appSrc *appv1.ApplicationSource, res interface{}) error {
	return c.getItem(appDetailsCacheKey(commitSHA, appSrc), res)
}

func (c *Cache) SetAppDetails(commitSHA string, appSrc *appv1.ApplicationSource, res interface{}) error {
	return c.setItem(appDetailsCacheKey(commitSHA, appSrc), res, repoCacheExpiration, res == nil)
}

func (c *Cache) GetRevisionMetadata(repoURL, revision string) (*appv1.RevisionMetadata, error) {
	item := &appv1.RevisionMetadata{}
	return item, c.getItem(revisionMetadataKey(repoURL, revision), item)
}

func (c *Cache) SetRevisionMetadata(repoURL, revision string, item *appv1.RevisionMetadata) error {
	return c.setItem(revisionMetadataKey(repoURL, revision), item, repoCacheExpiration, false)
}

func (c *Cache) GetOIDCState(key string) (*OIDCState, error) {
	res := OIDCState{}
	err := c.getItem(oidcStateKey(key), &res)
	return &res, err
}

func (c *Cache) SetOIDCState(key string, state *OIDCState) error {
	return c.setItem(oidcStateKey(key), state, oidcCacheExpiration, state == nil)
}
