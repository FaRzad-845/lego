package cmd

import (
	"crypto"
	"crypto/x509"
	"time"

	"github.com/go-acme/lego/v3/certcrypto"
	"github.com/go-acme/lego/v3/certificate"
	"github.com/go-acme/lego/v3/lego"
	"github.com/go-acme/lego/v3/log"
	"github.com/urfave/cli"
)

const (
	renewEnvAccountEmail = "LEGO_ACCOUNT_EMAIL"
	renewEnvCertDomain   = "LEGO_CERT_DOMAIN"
	renewEnvCertPath     = "LEGO_CERT_PATH"
	renewEnvCertKeyPath  = "LEGO_CERT_KEY_PATH"
)

func createRenew() cli.Command {
	return cli.Command{
		Name:   "renew",
		Usage:  "Renew a certificate",
		Action: renew,
		Before: func(ctx *cli.Context) error {
			// we require either domains or csr, but not both
			hasDomains := len(ctx.GlobalStringSlice("domains")) > 0
			hasCsr := len(ctx.GlobalString("csr")) > 0
			if hasDomains && hasCsr {
				log.Fatal("Please specify either --domains/-d or --csr/-c, but not both")
			}
			if !hasDomains && !hasCsr {
				log.Fatal("Please specify --domains/-d (or --csr/-c if you already have a CSR)")
			}
			return nil
		},
		Flags: []cli.Flag{
			cli.IntFlag{
				Name:  "days",
				Value: 30,
				Usage: "The number of days left on a certificate to renew it.",
			},
			cli.BoolFlag{
				Name:  "reuse-key",
				Usage: "Used to indicate you want to reuse your current private key for the new certificate.",
			},
			cli.BoolFlag{
				Name:  "no-bundle",
				Usage: "Do not create a certificate bundle by adding the issuers certificate to the new certificate.",
			},
			cli.BoolFlag{
				Name:  "must-staple",
				Usage: "Include the OCSP must staple TLS extension in the CSR and generated certificate. Only works if the CSR is generated by lego.",
			},
			cli.StringFlag{
				Name:  "renew-hook",
				Usage: "Define a hook. The hook is executed only when the certificates are effectively renewed.",
			},
		},
	}
}

func renew(ctx *cli.Context) error {
	account, client := setup(ctx, NewAccountsStorage(ctx))
	setupChallenges(ctx, client)

	if account.Registration == nil {
		log.Fatalf("Account %s is not registered. Use 'run' to register a new account.\n", account.Email)
	}

	certsStorage := NewCertificatesStorage(ctx)

	bundle := !ctx.Bool("no-bundle")

	meta := map[string]string{renewEnvAccountEmail: account.Email}

	// CSR
	if ctx.GlobalIsSet("csr") {
		return renewForCSR(ctx, client, certsStorage, bundle, meta)
	}

	// Domains
	return renewForDomains(ctx, client, certsStorage, bundle, meta)
}

func renewForDomains(ctx *cli.Context, client *lego.Client, certsStorage *CertificatesStorage, bundle bool, meta map[string]string) error {
	domains := ctx.GlobalStringSlice("domains")
	domain := domains[0]

	// load the cert resource from files.
	// We store the certificate, private key and metadata in different files
	// as web servers would not be able to work with a combined file.
	certificates, err := certsStorage.ReadCertificate(domain, ".crt")
	if err != nil {
		log.Fatalf("Error while loading the certificate for domain %s\n\t%v", domain, err)
	}

	cert := certificates[0]

	if !needRenewal(cert, domain, ctx.Int("days")) {
		return nil
	}

	// This is just meant to be informal for the user.
	timeLeft := cert.NotAfter.Sub(time.Now().UTC())
	log.Infof("[%s] acme: Trying renewal with %d hours remaining", domain, int(timeLeft.Hours()))

	certDomains := certcrypto.ExtractDomains(cert)

	var privateKey crypto.PrivateKey
	if ctx.Bool("reuse-key") {
		keyBytes, errR := certsStorage.ReadFile(domain, ".key")
		if errR != nil {
			log.Fatalf("Error while loading the private key for domain %s\n\t%v", domain, errR)
		}

		privateKey, errR = certcrypto.ParsePEMPrivateKey(keyBytes)
		if errR != nil {
			return errR
		}
	}

	request := certificate.ObtainRequest{
		Domains:    merge(certDomains, domains),
		Bundle:     bundle,
		PrivateKey: privateKey,
		MustStaple: ctx.Bool("must-staple"),
	}
	certRes, err := client.Certificate.Obtain(request)
	if err != nil {
		log.Fatal(err)
	}

	certsStorage.SaveResource(certRes)

	meta[renewEnvCertDomain] = domain
	meta[renewEnvCertPath] = certsStorage.GetFileName(domain, ".crt")
	meta[renewEnvCertKeyPath] = certsStorage.GetFileName(domain, ".key")

	return launchHook(ctx.String("renew-hook"), meta)
}

func renewForCSR(ctx *cli.Context, client *lego.Client, certsStorage *CertificatesStorage, bundle bool, meta map[string]string) error {
	csr, err := readCSRFile(ctx.GlobalString("csr"))
	if err != nil {
		log.Fatal(err)
	}

	domain := csr.Subject.CommonName

	// load the cert resource from files.
	// We store the certificate, private key and metadata in different files
	// as web servers would not be able to work with a combined file.
	certificates, err := certsStorage.ReadCertificate(domain, ".crt")
	if err != nil {
		log.Fatalf("Error while loading the certificate for domain %s\n\t%v", domain, err)
	}

	cert := certificates[0]

	if !needRenewal(cert, domain, ctx.Int("days")) {
		return nil
	}

	// This is just meant to be informal for the user.
	timeLeft := cert.NotAfter.Sub(time.Now().UTC())
	log.Infof("[%s] acme: Trying renewal with %d hours remaining", domain, int(timeLeft.Hours()))

	certRes, err := client.Certificate.ObtainForCSR(*csr, bundle)
	if err != nil {
		log.Fatal(err)
	}

	certsStorage.SaveResource(certRes)

	meta[renewEnvCertDomain] = domain
	meta[renewEnvCertPath] = certsStorage.GetFileName(domain, ".crt")
	meta[renewEnvCertKeyPath] = certsStorage.GetFileName(domain, ".key")

	return launchHook(ctx.String("renew-hook"), meta)
}

func needRenewal(x509Cert *x509.Certificate, domain string, days int) bool {
	if x509Cert.IsCA {
		log.Fatalf("[%s] Certificate bundle starts with a CA certificate", domain)
	}

	if days >= 0 {
		notAfter := int(time.Until(x509Cert.NotAfter).Hours() / 24.0)
		if notAfter > days {
			log.Printf("[%s] The certificate expires in %d days, the number of days defined to perform the renewal is %d: no renewal.",
				domain, notAfter, days)
			return false
		}
	}

	return true
}

func merge(prevDomains []string, nextDomains []string) []string {
	for _, next := range nextDomains {
		var found bool
		for _, prev := range prevDomains {
			if prev == next {
				found = true
				break
			}
		}
		if !found {
			prevDomains = append(prevDomains, next)
		}
	}
	return prevDomains
}
