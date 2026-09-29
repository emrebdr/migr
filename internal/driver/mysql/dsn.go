package mysql

import (
	sqldriver "database/sql/driver"
	"fmt"
	"strings"

	lib "github.com/go-sql-driver/mysql"

	"github.com/emrebdr/migr/internal/version"
)

// The connection-string flags this driver owns, because each changes what a statement or a value means.
const (
	// flagClientFoundRows would make Perform report rows matched where MySQL reports rows changed.
	flagClientFoundRows = "clientFoundRows"
	// flagMultiStatements would let one Query carry several, which read-only enforcement reasons against.
	flagMultiStatements = "multiStatements"
	// flagParseTime would hand some values over as time.Time, where every value is the server's own text.
	flagParseTime = "parseTime"
	// flagInterpolateParams would splice arguments into the statement rather than bind them.
	flagInterpolateParams = "interpolateParams"
)

// ownedFlags lists them in the order a refusal renders them.
var ownedFlags = []string{flagClientFoundRows, flagMultiStatements, flagParseTime,
	flagInterpolateParams}

// attributeName is what this session calls itself to anyone reading the server's connection attributes.
//
// MySQL has no application_name. A connection attribute is the nearest thing, and an ordinary user
// cannot read performance_schema.session_connect_attrs back -- measured, on both servers, as a 1142
// denial -- so this is for whoever holds those grants rather than something migr can rely on. M9-T4's
// LockHolder therefore reads information_schema.PROCESSLIST instead.
const attributeName = "program_name"

// parsed is a connection string this driver has read, checked, and is willing to open.
type parsed struct {
	// config is the library's own view of it, with what this driver pins already applied.
	config *lib.Config
	// database is the schema every unqualified name in a migration resolves against.
	database string
}

// parseDSN reports the connection to open, or why this driver will not open the one it was given.
func parseDSN(dsn, schema string) (parsed, error) {
	if strings.TrimSpace(dsn) == "" {
		return parsed{}, &DSNError{Detail: "it is empty, and a MySQL connection string names the " +
			"server, the credentials, and the database a migration's unqualified names resolve against"}
	}

	config, err := lib.ParseDSN(dsn)
	if err != nil {
		return parsed{}, &DSNError{Err: err, Detail: "the driver could not read it; the form is " +
			"user:password@tcp(host:port)/database"}
	}

	if err := checkParsed(config, schema); err != nil {
		return parsed{}, err
	}

	pinAttributes(config)

	return parsed{config: config, database: config.DBName}, nil
}

// checkParsed refuses a connection string this driver would have to silently override to use.
func checkParsed(config *lib.Config, schema string) error {
	if err := checkFlags(config); err != nil {
		return err
	}

	if err := checkParams(config); err != nil {
		return err
	}

	return checkDatabase(config, schema)
}

// checkFlags refuses a connection string setting one of the flags this driver owns.
func checkFlags(config *lib.Config) error {
	for _, flag := range setFlags(config) {
		return &DSNError{Detail: "it sets " + flag + "=true, which this driver owns (" +
			quoteList(ownedFlags) + "): each one changes what a statement or a value means, so " +
			"overriding the operator silently would be worse than refusing"}
	}

	return nil
}

// setFlags names the owned flags a connection string turned on, in the order a refusal renders them.
func setFlags(config *lib.Config) []string {
	var set []string

	for _, flag := range ownedFlags {
		if flagOf(config, flag) {
			set = append(set, flag)
		}
	}

	return set
}

// flagOf reports one owned flag's value, which the library carries as a field rather than a param.
func flagOf(config *lib.Config, flag string) bool {
	switch flag {
	case flagClientFoundRows:
		return config.ClientFoundRows
	case flagMultiStatements:
		return config.MultiStatements
	case flagParseTime:
		return config.ParseTime
	case flagInterpolateParams:
		return config.InterpolateParams
	default:
		return false
	}
}

// checkParams refuses a connection string setting a session variable this driver sets itself.
func checkParams(config *lib.Config) error {
	for name := range config.Params {
		if !ownsVariable(name) {
			continue
		}

		return &DSNError{Detail: "it sets " + strings.ToLower(name) + ", which this driver sets for " +
			"every session (" + quoteList(ownedVariables) + "): each is read back after it is set, so " +
			"a value the connection string asked for would be overwritten without anyone being told"}
	}

	return nil
}

// checkDatabase refuses a connection string with no database, or one a configured schema contradicts.
func checkDatabase(config *lib.Config, schema string) error {
	if config.DBName == "" {
		return &DSNError{Detail: "it names no database, and MySQL resolves every unqualified name " +
			"against one: the first statement of the first migration would fail with 'No database " +
			"selected' after the lock had already been taken"}
	}

	if strings.TrimSpace(schema) == "" || schema == config.DBName {
		return nil
	}

	return &SchemaError{Schema: schema, Database: config.DBName}
}

// pinAttributes makes this session recognizable to whoever can read the server's connection attributes.
func pinAttributes(config *lib.Config) {
	config.ConnectionAttributes = attributeName + ":" + applicationName()
}

// applicationName is what a migr session calls itself, with the two bytes an attribute list uses removed.
func applicationName() string {
	return attributeSafe(version.Program + " " + version.Version)
}

// attributeSafe replaces the separators a comma-delimited key:value list is parsed on.
func attributeSafe(name string) string {
	return strings.Map(func(r rune) rune {
		if r == ',' || r == ':' {
			return '-'
		}

		return r
	}, name)
}

// connector turns a checked connection string into what database/sql opens a pool over.
func (p parsed) connector() (sqldriver.Connector, error) {
	built, err := lib.NewConnector(p.config)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConnect, err)
	}

	return built, nil
}
