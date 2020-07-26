package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/niclabs/dns-tools/tools"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Defaults for ZSK and KSK signature expirations
var (
	DefaultKSKExpiration   time.Time = time.Now().AddDate(1, 0, 0)
	DefaultZSKExpiration   time.Time = time.Now().AddDate(1, 0, 0)
	DefaultRRSigExpiration time.Time = time.Now().AddDate(0, 3, 0)
)

func init() {
	signCmd.PersistentFlags().StringP("file", "f", "", "Full path to zone file to be signed.")
	signCmd.PersistentFlags().StringP("zone", "z", "", "Origin zone name. If it is not specified, $ORIGIN inside the file will be used as this value.")
	signCmd.PersistentFlags().StringP("output", "o", "", "Output for the signed zone file. By default is based on zone file name, with \"-signed\" at the end of the name and before the extension")
	signCmd.PersistentFlags().BoolP("create-keys", "c", false, "Creates a new pair of keys, outdating all valid keys.")
	signCmd.PersistentFlags().StringP("sign-algorithm", "a", "rsa", "Algorithm used in signing.")
	signCmd.PersistentFlags().BoolP("nsec3", "3", false, "Use NSEC3 instead of NSEC.")
	signCmd.PersistentFlags().BoolP("opt-out", "x", false, "Use NSEC3 with opt-out.")
	signCmd.PersistentFlags().BoolP("digest", "d", false, "If true, DigestEnabled RR is added to the signed zone")
	signCmd.PersistentFlags().BoolP("info", "i", false, "If true, an TXT RR is added with information about the signing process (tool and mode)")

	signCmd.PersistentFlags().String("zsk-expiration-date", "", "ZSK Key expiration Date, in YYYYMMDD format. It is ignored if --zsk-duration is set. Default is three months from now.")
	signCmd.PersistentFlags().String("ksk-expiration-date", "", "KSK Key expiration Date, in YYYYMMDD format. It is ignored if --ksk-duration is set. Default is one year from now.")
	signCmd.PersistentFlags().String("rrsig-expiration-date", "", "RRSIG expiration Date, in YYYYMMDD format. It is ignored if --ksk-duration is set. Default is three months from now.")
	signCmd.PersistentFlags().String("zsk-duration", "", "Relative ZSK Key expiration Date, in human readable format (combining numbers with labels like year(s), month(s), day(s), hour(s), minute(s), second(s)). Overrides --ksk-date-expiration. Default is empty.")
	signCmd.PersistentFlags().String("ksk-duration", "", "Relative KSK Key expiration Date, in human readable format (combining numbers with labels like year(s), month(s), day(s), hour(s), minute(s), second(s)). Overrides --zsk-date-expiration. Default is empty.")
	signCmd.PersistentFlags().String("rrsig-duration", "", "Relative RRSIG expiration Date, in human readable format (combining numbers with labels like year(s), month(s), day(s), hour(s), minute(s), second(s)). Overrides --rrsig-date-expiration. Default is empty.")

	pkcs11Cmd.PersistentFlags().StringP("user-key", "k", "1234", "HSM User Login PKCS11Key.")
	pkcs11Cmd.PersistentFlags().StringP("key-label", "l", "HSM-tools", "Label of HSM Signer PKCS11Key.")
	pkcs11Cmd.PersistentFlags().StringP("p11lib", "p", "", "Full path to PKCS11Type lib file.")
	signCmd.AddCommand(pkcs11Cmd)

	fileCmd.PersistentFlags().StringP("zsk-keyfile", "Z", "zsk.pem", "Full path to ZSK key file.")
	fileCmd.PersistentFlags().StringP("ksk-keyfile", "K", "ksk.pem", "Full path to KSK key file.")
	signCmd.AddCommand(fileCmd)
}

var signCmd = &cobra.Command{
	Use:   "sign",
	Short: "Signs a DNS Zone using a PKCS#11 library or a file",
}

var pkcs11Cmd = &cobra.Command{
	Use:   "pkcs11",
	Short: "uses a PKCS#11 library to sign the zone",
	RunE:  signPKCS11,
}

var fileCmd = &cobra.Command{
	Use:   "file",
	Short: "uses keys from a file to sign the zone",
	RunE:  signFile,
}

func signPKCS11(cmd *cobra.Command, _ []string) error {
	if err := viper.BindPFlags(cmd.Flags()); err != nil {
		return err
	}
	conf, err := newSignConfig()
	if err != nil {
		return err
	}
	ctx, err := tools.NewContext(conf, commandLog)
	if err != nil {
		return err
	}
	defer ctx.Close()
	p11lib := viper.GetString("p11lib")
	if len(p11lib) == 0 {
		return fmt.Errorf("p11lib not specified")
	}
	key := viper.GetString("user-key")
	if len(key) == 0 {
		return fmt.Errorf("user-key not specified")
	}
	label := viper.GetString("key-label")
	if len(label) == 0 {
		return fmt.Errorf("key-label not specified")
	}
	if err := filesExist(p11lib); err != nil {
		return err
	}
	session, err := ctx.NewPKCS11Session(key, label, p11lib)
	if err != nil {
		return err
	}
	defer session.End()
	if _, err := tools.Sign(session); err != nil {
		ctx.Log.Printf("zone could not be signed.")
		return err
	}
	ctx.Log.Printf("zone signed successfully.")
	return nil
}

func signFile(cmd *cobra.Command, _ []string) error {
	if err := viper.BindPFlags(cmd.Flags()); err != nil {
		return err
	}
	conf, err := newSignConfig()
	if err != nil {
		return err
	}
	ctx, err := tools.NewContext(conf, commandLog)
	defer ctx.Close()
	if err != nil {
		return err
	}
	zskKeypath := viper.GetString("ksk-keyfile")
	if len(zskKeypath) == 0 {
		return fmt.Errorf("ZSK keyfile not specified")
	}
	kskKeypath := viper.GetString("zsk-keyfile")
	if len(kskKeypath) == 0 {
		return fmt.Errorf("KSK keyfile not specified")
	}

	fileFlags := os.O_RDWR | os.O_CREATE
	if ctx.Config.CreateKeys {
		fileFlags |= os.O_TRUNC // Truncate old file
	}

	zskFile, err := os.OpenFile(zskKeypath, fileFlags, 0600)
	if err != nil {
		return err
	}
	kskFile, err := os.OpenFile(kskKeypath, fileFlags, 0600)
	if err != nil {
		return err
	}
	session, err := ctx.NewFileSession(kskFile, zskFile)
	if err != nil {
		return err
	}
	defer session.End()
	if _, err := tools.Sign(session); err != nil {
		return err
	}
	ctx.Log.Printf("zone signed successfully.")
	return nil
}

func newSignConfig() (*tools.ContextConfig, error) {
	createKeys := viper.GetBool("create-keys")
	zone := viper.GetString("zone")
	nsec3 := viper.GetBool("nsec3")
	optOut := viper.GetBool("opt-out")
	digest := viper.GetBool("digest")
	info := viper.GetBool("info")

	path := viper.GetString("file")
	out := viper.GetString("output")

	signAlgorithm := viper.GetString("sign-algorithm")

	if len(path) == 0 {
		return nil, fmt.Errorf("zone file not specified")
	}
	if len(zone) == 0 {
		return nil, fmt.Errorf("zone not specified")
	}
	if len(out) == 0 {
		pathExt := filepath.Ext(path)
		pathName := strings.TrimSuffix(filepath.Base(path), pathExt)
		out = filepath.Join(filepath.Dir(path), pathName+"-signed"+pathExt)
	}

	if err := filesExist(path); err != nil {
		return nil, err
	}

	kskExpDate, err := getExpDate(viper.GetString("zsk-duration"), viper.GetString("zsk-expiration-date"), DefaultZSKExpiration)
	if err != nil {
		return nil, err
	}
	zskExpDate, err := getExpDate(viper.GetString("ksk-duration"), viper.GetString("ksk-expiration-date"), DefaultKSKExpiration)
	if err != nil {
		return nil, err
	}
	rrsigExpDate, err := getExpDate(viper.GetString("rrsig-duration"), viper.GetString("rrsig-expiration-date"), DefaultRRSigExpiration)
	if err != nil {
		return nil, err
	}
	return &tools.ContextConfig{
		Zone:          zone,
		CreateKeys:    createKeys,
		NSEC3:         nsec3,
		DigestEnabled: digest,
		OptOut:        optOut,
		SignAlgorithm: signAlgorithm,
		KSKExpDate:    kskExpDate,
		ZSKExpDate:    zskExpDate,
		RRSIGExpDate:  rrsigExpDate,
		FilePath:      path,
		OutputPath:    out,
		Info:          info,
	}, nil
}

func getExpDate(durString, expDate string, def time.Time) (time.Time, error) {
	if len(durString) > 0 {
		return tools.DurationToTime(time.Now(), durString)
	}
	if len(expDate) > 0 {
		return time.Parse("20060102", expDate)
	}
	return def, nil
}
