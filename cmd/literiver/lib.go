package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/benbjohnson/litestream"
	"github.com/benbjohnson/litestream/abs"
	"github.com/benbjohnson/litestream/file"
	"github.com/benbjohnson/litestream/gcs"
	"github.com/benbjohnson/litestream/s3"
	"github.com/benbjohnson/litestream/sftp"
)

// DBConfig represents the configuration for a single database.
type DBConfig struct {
	Path               string         `yaml:"path"`
	MetaPath           *string        `yaml:"meta-path"`
	MonitorInterval    *time.Duration `yaml:"monitor-interval"`
	CheckpointInterval *time.Duration `yaml:"checkpoint-interval"`
	MinCheckpointPageN *int           `yaml:"min-checkpoint-page-count"`
	MaxCheckpointPageN *int           `yaml:"max-checkpoint-page-count"`

	Replicas []*ReplicaConfig `yaml:"replicas"`
}

// NewDBFromConfig instantiates a DB based on a configuration.
func NewDBFromConfig(dbc *DBConfig) (*litestream.DB, error) {
	path, err := expand(dbc.Path)
	if err != nil {
		return nil, err
	}

	// Initialize database with given path.
	db := litestream.NewDB(path)

	// Override default database settings if specified in configuration.
	if dbc.MetaPath != nil {
		db.SetMetaPath(*dbc.MetaPath)
	}
	if dbc.MonitorInterval != nil {
		db.MonitorInterval = *dbc.MonitorInterval
	}
	if dbc.CheckpointInterval != nil {
		db.CheckpointInterval = *dbc.CheckpointInterval
	}
	if dbc.MinCheckpointPageN != nil {
		db.MinCheckpointPageN = *dbc.MinCheckpointPageN
	}
	if dbc.MaxCheckpointPageN != nil {
		db.MaxCheckpointPageN = *dbc.MaxCheckpointPageN
	}

	// Instantiate and attach replicas.
	for _, rc := range dbc.Replicas {
		r, err := NewReplicaFromConfig(rc, db)
		if err != nil {
			return nil, err
		}
		db.Replicas = append(db.Replicas, r)
	}

	return db, nil
}

// ReplicaConfig represents the configuration for a single replica in a database.
type ReplicaConfig struct {
	Type                   string         `yaml:"type"` // "file", "s3"
	Name                   string         `yaml:"name"` // name of replica, optional.
	Path                   string         `yaml:"path"`
	URL                    string         `yaml:"url"`
	Retention              *time.Duration `yaml:"retention"`
	RetentionCheckInterval *time.Duration `yaml:"retention-check-interval"`
	SyncInterval           *time.Duration `yaml:"sync-interval"`
	SnapshotInterval       *time.Duration `yaml:"snapshot-interval"`
	ValidationInterval     *time.Duration `yaml:"validation-interval"`

	// S3 settings
	AccessKeyID     string `yaml:"access-key-id"`
	SecretAccessKey string `yaml:"secret-access-key"`
	Region          string `yaml:"region"`
	Bucket          string `yaml:"bucket"`
	Endpoint        string `yaml:"endpoint"`
	ForcePathStyle  *bool  `yaml:"force-path-style"`
	SkipVerify      bool   `yaml:"skip-verify"`

	// ABS settings
	AccountName string `yaml:"account-name"`
	AccountKey  string `yaml:"account-key"`

	// SFTP settings
	Host     string `yaml:"host"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	KeyPath  string `yaml:"key-path"`

	// Encryption identities and recipients
	Age struct {
		Identities []string `yaml:"identities"`
		Recipients []string `yaml:"recipients"`
	} `yaml:"age"`
}

// NewReplicaFromConfig instantiates a replica for a DB based on a config.
func NewReplicaFromConfig(c *ReplicaConfig, db *litestream.DB) (_ *litestream.Replica, err error) {
	// Ensure user did not specify URL in path.
	if isURL(c.Path) {
		return nil, fmt.Errorf("replica path cannot be a url, please use the 'url' field instead: %s", c.Path)
	}

	// Build replica.
	r := litestream.NewReplica(db, c.Name)
	if v := c.Retention; v != nil {
		r.Retention = *v
	}
	if v := c.RetentionCheckInterval; v != nil {
		r.RetentionCheckInterval = *v
	}
	if v := c.SyncInterval; v != nil {
		r.SyncInterval = *v
	}
	if v := c.SnapshotInterval; v != nil {
		r.SnapshotInterval = *v
	}
	if v := c.ValidationInterval; v != nil {
		r.ValidationInterval = *v
	}
	for _, str := range c.Age.Identities {
		identities, err := age.ParseIdentities(strings.NewReader(str))
		if err != nil {
			return nil, err
		}

		r.AgeIdentities = append(r.AgeIdentities, identities...)
	}
	for _, str := range c.Age.Recipients {
		recipients, err := age.ParseRecipients(strings.NewReader(str))
		if err != nil {
			return nil, err
		}

		r.AgeRecipients = append(r.AgeRecipients, recipients...)
	}

	// Build and set client on replica.
	switch c.ReplicaType() {
	case "file":
		if r.Client, err = newFileReplicaClientFromConfig(c, r); err != nil {
			return nil, err
		}
	case "s3":
		if r.Client, err = newS3ReplicaClientFromConfig(c, r); err != nil {
			return nil, err
		}
	case "gcs":
		if r.Client, err = newGCSReplicaClientFromConfig(c, r); err != nil {
			return nil, err
		}
	case "abs":
		if r.Client, err = newABSReplicaClientFromConfig(c, r); err != nil {
			return nil, err
		}
	case "sftp":
		if r.Client, err = newSFTPReplicaClientFromConfig(c, r); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown replica type in config: %q", c.Type)
	}

	return r, nil
}

// newFileReplicaClientFromConfig returns a new instance of file.ReplicaClient built from config.
func newFileReplicaClientFromConfig(c *ReplicaConfig, r *litestream.Replica) (_ *file.ReplicaClient, err error) {
	// Ensure URL & path are not both specified.
	if c.URL != "" && c.Path != "" {
		return nil, fmt.Errorf("cannot specify url & path for file replica")
	}

	// Parse path from URL, if specified.
	path := c.Path
	if c.URL != "" {
		if _, _, path, err = ParseReplicaURL(c.URL); err != nil {
			return nil, err
		}
	}

	// Ensure path is set explicitly or derived from URL field.
	if path == "" {
		return nil, fmt.Errorf("file replica path required")
	}

	// Expand home prefix and return absolute path.
	if path, err = expand(path); err != nil {
		return nil, err
	}

	// Instantiate replica and apply time fields, if set.
	client := file.NewReplicaClient(path)
	client.Replica = r
	return client, nil
}

// newS3ReplicaClientFromConfig returns a new instance of s3.ReplicaClient built from config.
func newS3ReplicaClientFromConfig(c *ReplicaConfig, r *litestream.Replica) (_ *s3.ReplicaClient, err error) {
	// Ensure URL & constituent parts are not both specified.
	if c.URL != "" && c.Path != "" {
		return nil, fmt.Errorf("cannot specify url & path for s3 replica")
	} else if c.URL != "" && c.Bucket != "" {
		return nil, fmt.Errorf("cannot specify url & bucket for s3 replica")
	}

	bucket, path := c.Bucket, c.Path
	region, endpoint, skipVerify := c.Region, c.Endpoint, c.SkipVerify

	// Use path style if an endpoint is explicitly set. This works because the
	// only service to not use path style is AWS which does not use an endpoint.
	forcePathStyle := (endpoint != "")
	if v := c.ForcePathStyle; v != nil {
		forcePathStyle = *v
	}

	// Apply settings from URL, if specified.
	if c.URL != "" {
		_, host, upath, err := ParseReplicaURL(c.URL)
		if err != nil {
			return nil, err
		}
		ubucket, uregion, uendpoint, uforcePathStyle := s3.ParseHost(host)

		// Only apply URL parts to field that have not been overridden.
		if path == "" {
			path = upath
		}
		if bucket == "" {
			bucket = ubucket
		}
		if region == "" {
			region = uregion
		}
		if endpoint == "" {
			endpoint = uendpoint
		}
		if !forcePathStyle {
			forcePathStyle = uforcePathStyle
		}
	}

	// Ensure required settings are set.
	if bucket == "" {
		return nil, fmt.Errorf("bucket required for s3 replica")
	}

	// Build replica.
	client := s3.NewReplicaClient()
	client.AccessKeyID = c.AccessKeyID
	client.SecretAccessKey = c.SecretAccessKey
	client.Bucket = bucket
	client.Path = path
	client.Region = region
	client.Endpoint = endpoint
	client.ForcePathStyle = forcePathStyle
	client.SkipVerify = skipVerify
	return client, nil
}

// newGCSReplicaClientFromConfig returns a new instance of gcs.ReplicaClient built from config.
func newGCSReplicaClientFromConfig(c *ReplicaConfig, r *litestream.Replica) (_ *gcs.ReplicaClient, err error) {
	// Ensure URL & constituent parts are not both specified.
	if c.URL != "" && c.Path != "" {
		return nil, fmt.Errorf("cannot specify url & path for gcs replica")
	} else if c.URL != "" && c.Bucket != "" {
		return nil, fmt.Errorf("cannot specify url & bucket for gcs replica")
	}

	bucket, path := c.Bucket, c.Path

	// Apply settings from URL, if specified.
	if c.URL != "" {
		_, uhost, upath, err := ParseReplicaURL(c.URL)
		if err != nil {
			return nil, err
		}

		// Only apply URL parts to field that have not been overridden.
		if path == "" {
			path = upath
		}
		if bucket == "" {
			bucket = uhost
		}
	}

	// Ensure required settings are set.
	if bucket == "" {
		return nil, fmt.Errorf("bucket required for gcs replica")
	}

	// Build replica.
	client := gcs.NewReplicaClient()
	client.Bucket = bucket
	client.Path = path
	return client, nil
}

// newABSReplicaClientFromConfig returns a new instance of abs.ReplicaClient built from config.
func newABSReplicaClientFromConfig(c *ReplicaConfig, r *litestream.Replica) (_ *abs.ReplicaClient, err error) {
	// Ensure URL & constituent parts are not both specified.
	if c.URL != "" && c.Path != "" {
		return nil, fmt.Errorf("cannot specify url & path for abs replica")
	} else if c.URL != "" && c.Bucket != "" {
		return nil, fmt.Errorf("cannot specify url & bucket for abs replica")
	}

	// Build replica.
	client := abs.NewReplicaClient()
	client.AccountName = c.AccountName
	client.AccountKey = c.AccountKey
	client.Bucket = c.Bucket
	client.Path = c.Path
	client.Endpoint = c.Endpoint

	// Apply settings from URL, if specified.
	if c.URL != "" {
		u, err := url.Parse(c.URL)
		if err != nil {
			return nil, err
		}

		if client.AccountName == "" && u.User != nil {
			client.AccountName = u.User.Username()
		}
		if client.Bucket == "" {
			client.Bucket = u.Host
		}
		if client.Path == "" {
			client.Path = strings.TrimPrefix(path.Clean(u.Path), "/")
		}
	}

	// Ensure required settings are set.
	if client.Bucket == "" {
		return nil, fmt.Errorf("bucket required for abs replica")
	}

	return client, nil
}

// newSFTPReplicaClientFromConfig returns a new instance of sftp.ReplicaClient built from config.
func newSFTPReplicaClientFromConfig(c *ReplicaConfig, r *litestream.Replica) (_ *sftp.ReplicaClient, err error) {
	// Ensure URL & constituent parts are not both specified.
	if c.URL != "" && c.Path != "" {
		return nil, fmt.Errorf("cannot specify url & path for sftp replica")
	} else if c.URL != "" && c.Host != "" {
		return nil, fmt.Errorf("cannot specify url & host for sftp replica")
	}

	host, user, password, path := c.Host, c.User, c.Password, c.Path

	// Apply settings from URL, if specified.
	if c.URL != "" {
		u, err := url.Parse(c.URL)
		if err != nil {
			return nil, err
		}

		// Only apply URL parts to field that have not been overridden.
		if host == "" {
			host = u.Host
		}
		if user == "" && u.User != nil {
			user = u.User.Username()
		}
		if password == "" && u.User != nil {
			password, _ = u.User.Password()
		}
		if path == "" {
			path = u.Path
		}
	}

	// Ensure required settings are set.
	if host == "" {
		return nil, fmt.Errorf("host required for sftp replica")
	} else if user == "" {
		return nil, fmt.Errorf("user required for sftp replica")
	}

	// Build replica.
	client := sftp.NewReplicaClient()
	client.Host = host
	client.User = user
	client.Password = password
	client.Path = path
	client.KeyPath = c.KeyPath
	return client, nil
}

// applyLitestreamEnv copies "LITESTREAM" prefixed environment variables to
// their AWS counterparts as the "AWS" prefix can be confusing when using a
// non-AWS S3-compatible service.
func applyLitestreamEnv() {
	if v, ok := os.LookupEnv("LITESTREAM_ACCESS_KEY_ID"); ok {
		if _, ok := os.LookupEnv("AWS_ACCESS_KEY_ID"); !ok {
			os.Setenv("AWS_ACCESS_KEY_ID", v)
		}
	}
	if v, ok := os.LookupEnv("LITESTREAM_SECRET_ACCESS_KEY"); ok {
		if _, ok := os.LookupEnv("AWS_SECRET_ACCESS_KEY"); !ok {
			os.Setenv("AWS_SECRET_ACCESS_KEY", v)
		}
	}
}

// ParseReplicaURL parses a replica URL.
func ParseReplicaURL(s string) (scheme, host, urlpath string, err error) {
	u, err := url.Parse(s)
	if err != nil {
		return "", "", "", err
	}

	switch u.Scheme {
	case "file":
		scheme, u.Scheme = u.Scheme, ""
		return scheme, "", path.Clean(u.String()), nil

	case "":
		return u.Scheme, u.Host, u.Path, fmt.Errorf("replica url scheme required: %s", s)

	default:
		return u.Scheme, u.Host, strings.TrimPrefix(path.Clean(u.Path), "/"), nil
	}
}

// isURL returns true if s can be parsed and has a scheme.
func isURL(s string) bool {
	return regexp.MustCompile(`^\w+:\/\/`).MatchString(s)
}

// ReplicaType returns the type based on the type field or extracted from the URL.
func (c *ReplicaConfig) ReplicaType() string {
	scheme, _, _, _ := ParseReplicaURL(c.URL)
	if scheme != "" {
		return scheme
	} else if c.Type != "" {
		return c.Type
	}
	return "file"
}

func registerConfigFlag(fs *flag.FlagSet) (configPath *string, noExpandEnv *bool) {
	return fs.String("config", "", "config path"),
		fs.Bool("no-expand-env", false, "do not expand env vars in config")
}

// expand returns an absolute path for s.
func expand(s string) (string, error) {
	// Just expand to absolute path if there is no home directory prefix.
	prefix := "~" + string(os.PathSeparator)
	if s != "~" && !strings.HasPrefix(s, prefix) {
		return filepath.Abs(s)
	}

	// Look up home directory.
	u, err := user.Current()
	if err != nil {
		return "", err
	} else if u.HomeDir == "" {
		return "", fmt.Errorf("cannot expand path %s, no home directory available", s)
	}

	// Return path with tilde replaced by the home directory.
	if s == "~" {
		return u.HomeDir, nil
	}
	return filepath.Join(u.HomeDir, strings.TrimPrefix(s, prefix)), nil
}

// indexVar allows the flag package to parse index flags as 4-byte hexadecimal values.
type indexVar int

// Ensure type implements interface.
var _ flag.Value = (*indexVar)(nil)

// String returns an 8-character hexadecimal value.
func (v *indexVar) String() string {
	return fmt.Sprintf("%08x", int(*v))
}

// Set parses s into an integer from a hexadecimal value.
func (v *indexVar) Set(s string) error {
	i, err := strconv.ParseInt(s, 16, 32)
	if err != nil {
		return fmt.Errorf("invalid hexadecimal format")
	}
	*v = indexVar(i)
	return nil
}