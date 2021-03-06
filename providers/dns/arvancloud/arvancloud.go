package arvancloud

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-acme/lego/challenge/dns01"
	"github.com/go-acme/lego/platform/config/env"
	"github.com/go-acme/lego/v3/providers/dns/arvancloud/internal"
)

const minTTL = 600

// Environment variables names.
const (
	envNamespace = "ARVANCLOUD_"

	EnvAPIKey = envNamespace + "API_KEY"

	EnvTTL                = envNamespace + "TTL"
	EnvPropagationTimeout = envNamespace + "PROPAGATION_TIMEOUT"
	EnvPollingInterval    = envNamespace + "POLLING_INTERVAL"
	EnvHTTPTimeout        = envNamespace + "HTTP_TIMEOUT"
)

// Config is used to configure the creation of the DNSProvider.
type Config struct {
	APIKey             string
	PropagationTimeout time.Duration
	PollingInterval    time.Duration
	TTL                int
	HTTPClient         *http.Client
}

// NewDefaultConfig returns a default configuration for the DNSProvider.
func NewDefaultConfig() *Config {
	return &Config{
		TTL:                env.GetOrDefaultInt(EnvTTL, minTTL),
		PropagationTimeout: env.GetOrDefaultSecond(EnvPropagationTimeout, 120*time.Second),
		PollingInterval:    env.GetOrDefaultSecond(EnvPollingInterval, 2*time.Second),
		HTTPClient: &http.Client{
			Timeout: env.GetOrDefaultSecond(EnvHTTPTimeout, 30*time.Second),
		},
	}
}

// DNSProvider implements the challenge.Provider interface.
type DNSProvider struct {
	config *Config
	client *internal.Client
}

// NewDNSProvider returns a DNSProvider instance configured for ArvanCloud.
// Credentials must be passed in the environment variable: ARVANCLOUD_API_KEY.
func NewDNSProvider() (*DNSProvider, error) {
	values, err := env.Get(EnvAPIKey)
	if err != nil {
		return nil, fmt.Errorf("arvanCloud: %w", err)
	}

	config := NewDefaultConfig()
	config.APIKey = values[EnvAPIKey]

	return NewDNSProviderConfig(config)
}

// NewDNSProviderConfig return a DNSProvider instance configured for arvanCloud.
func NewDNSProviderConfig(config *Config) (*DNSProvider, error) {
	if config == nil {
		return nil, errors.New("arvanCloud: the configuration of the DNS provider is nil")
	}

	if config.APIKey == "" {
		return nil, errors.New("arvanCloud: credentials missing")
	}

	if config.TTL < minTTL {
		return nil, fmt.Errorf("arvanCloud: invalid TTL, TTL (%d) must be greater than %d", config.TTL, minTTL)
	}

	client := internal.NewClient(config.APIKey)

	if config.HTTPClient != nil {
		client.HTTPClient = config.HTTPClient
	}

	return &DNSProvider{config: config, client: client}, nil
}

// Timeout returns the timeout and interval to use when checking for DNS
// propagation. Adjusting here to cope with spikes in propagation times.
func (d *DNSProvider) Timeout() (timeout, interval time.Duration) {
	return d.config.PropagationTimeout, d.config.PollingInterval
}

// Present creates a TXT record to fulfill the dns-01 challenge.
func (d *DNSProvider) Present(domain, token, keyAuth string) error {
	fqdn, value := dns01.GetRecord(domain, keyAuth)

	record := internal.DNSRecord{
		Type:          "txt",
		Name:          d.extractRecordName(fqdn, domain),
		Value:         internal.TxtValue{Text: value},
		TTL:           d.config.TTL,
		UpstreamHTTPS: "default",
		IPFilterMode: internal.IPFilterMode{
			Count:     "single",
			GeoFilter: "none",
			Order:     "none",
		},
	}

	if err := d.client.CreateRecord(domain, record); err != nil {
		return fmt.Errorf("arvanCloud: failed to add TXT record: fqdn=%s, domain name=%s: %w", fqdn, domain, err)
	}

	return nil
}

// CleanUp removes the TXT record matching the specified parameters.
func (d *DNSProvider) CleanUp(domain, token, keyAuth string) error {
	fqdn, value := dns01.GetRecord(domain, keyAuth)

	recordName := d.extractRecordName(fqdn, domain)

	record, err := d.client.GetTxtRecord(domain, recordName, value)
	if err != nil {
		return fmt.Errorf("arvanCloud: %w", err)
	}

	if err := d.client.DeleteRecord(domain, record.ID); err != nil {
		return fmt.Errorf("arvanCloud: failed to delate TXT record: id=%s, name=%s: %w", record.ID, record.Name, err)
	}

	return nil
}

func (d *DNSProvider) extractRecordName(fqdn, domain string) string {
	name := dns01.UnFqdn(fqdn)
	if idx := strings.Index(name, "."+domain); idx != -1 {
		return name[:idx]
	}
	return name
}
