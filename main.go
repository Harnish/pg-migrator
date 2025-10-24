package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	_ "github.com/lib/pq"
)

type Config struct {
	SrcHost     string
	SrcPort     int
	SrcUser     string
	SrcPassword string
	DstHost     string
	DstPort     int
	DstUser     string
	DstPassword string
	DumpDir     string
}

type Role struct {
	Name         string
	Super        bool
	Inherit      bool
	CreateRole   bool
	CreateDB     bool
	CanLogin     bool
	Replication  bool
	ConnLimit    int
	ValidUntil   sql.NullString
}

type Migrator struct {
	config    Config
	srcConn   *sql.DB
	dstConn   *sql.DB
}

func NewMigrator(config Config) *Migrator {
	return &Migrator{
		config: config,
	}
}

func (m *Migrator) Connect() error {
	var err error
	
	// Connect to source
	srcConnStr := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=postgres sslmode=disable",
		m.config.SrcHost, m.config.SrcPort, m.config.SrcUser, m.config.SrcPassword)
	
	m.srcConn, err = sql.Open("postgres", srcConnStr)
	if err != nil {
		return fmt.Errorf("failed to connect to source: %w", err)
	}
	
	if err = m.srcConn.Ping(); err != nil {
		return fmt.Errorf("failed to ping source: %w", err)
	}
	log.Println("✓ Connected to source server")
	
	// Connect to destination
	dstConnStr := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=postgres sslmode=disable",
		m.config.DstHost, m.config.DstPort, m.config.DstUser, m.config.DstPassword)
	
	m.dstConn, err = sql.Open("postgres", dstConnStr)
	if err != nil {
		return fmt.Errorf("failed to connect to destination: %w", err)
	}
	
	if err = m.dstConn.Ping(); err != nil {
		return fmt.Errorf("failed to ping destination: %w", err)
	}
	log.Println("✓ Connected to destination server")
	
	return nil
}

func (m *Migrator) Close() {
	if m.srcConn != nil {
		m.srcConn.Close()
	}
	if m.dstConn != nil {
		m.dstConn.Close()
	}
}

func (m *Migrator) GetRoles() ([]Role, error) {
	query := `
		SELECT 
			rolname, 
			rolsuper, 
			rolinherit, 
			rolcreaterole, 
			rolcreatedb, 
			rolcanlogin, 
			rolreplication,
			rolconnlimit,
			rolvaliduntil
		FROM pg_roles
		WHERE rolname NOT IN ('postgres', 'pg_monitor', 'pg_read_all_settings',
							  'pg_read_all_stats', 'pg_stat_scan_tables',
							  'pg_read_server_files', 'pg_write_server_files',
							  'pg_execute_server_program', 'pg_signal_backend')
		AND rolname NOT LIKE 'pg_%'
		ORDER BY rolname;
	`
	
	rows, err := m.srcConn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query roles: %w", err)
	}
	defer rows.Close()
	
	var roles []Role
	for rows.Next() {
		var r Role
		err := rows.Scan(&r.Name, &r.Super, &r.Inherit, &r.CreateRole, 
			&r.CreateDB, &r.CanLogin, &r.Replication, &r.ConnLimit, &r.ValidUntil)
		if err != nil {
			return nil, fmt.Errorf("failed to scan role: %w", err)
		}
		roles = append(roles, r)
	}
	
	return roles, nil
}

func (m *Migrator) GetRolePasswords() (map[string]string, error) {
	query := `
		SELECT rolname, rolpassword
		FROM pg_authid
		WHERE rolpassword IS NOT NULL
		AND rolname NOT IN ('postgres')
		AND rolname NOT LIKE 'pg_%';
	`
	
	rows, err := m.srcConn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query passwords: %w", err)
	}
	defer rows.Close()
	
	passwords := make(map[string]string)
	for rows.Next() {
		var name, password string
		if err := rows.Scan(&name, &password); err != nil {
			return nil, fmt.Errorf("failed to scan password: %w", err)
		}
		passwords[name] = password
	}
	
	return passwords, nil
}

func (m *Migrator) RoleExists(roleName string) (bool, error) {
	var exists bool
	query := "SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname = $1)"
	err := m.dstConn.QueryRow(query, roleName).Scan(&exists)
	return exists, err
}

func (m *Migrator) CreateRole(role Role, password string) error {
	exists, err := m.RoleExists(role.Name)
	if err != nil {
		return fmt.Errorf("failed to check if role exists: %w", err)
	}
	
	if exists {
		log.Printf("Role %s already exists, skipping...", role.Name)
		return nil
	}
	
	// Build CREATE ROLE statement
	var stmt strings.Builder
	fmt.Fprintf(&stmt, `CREATE ROLE "%s"`, role.Name)
	
	var options []string
	
	if role.Super {
		options = append(options, "SUPERUSER")
	} else {
		options = append(options, "NOSUPERUSER")
	}
	
	if role.Inherit {
		options = append(options, "INHERIT")
	} else {
		options = append(options, "NOINHERIT")
	}
	
	if role.CreateRole {
		options = append(options, "CREATEROLE")
	} else {
		options = append(options, "NOCREATEROLE")
	}
	
	if role.CreateDB {
		options = append(options, "CREATEDB")
	} else {
		options = append(options, "NOCREATEDB")
	}
	
	if role.CanLogin {
		options = append(options, "LOGIN")
	} else {
		options = append(options, "NOLOGIN")
	}
	
	if role.Replication {
		options = append(options, "REPLICATION")
	} else {
		options = append(options, "NOREPLICATION")
	}
	
	if role.ConnLimit != -1 {
		options = append(options, fmt.Sprintf("CONNECTION LIMIT %d", role.ConnLimit))
	}
	
	if len(options) > 0 {
		fmt.Fprintf(&stmt, " WITH %s", strings.Join(options, " "))
	}
	
	if password != "" {
		fmt.Fprintf(&stmt, " PASSWORD '%s'", password)
	}
	
	if role.ValidUntil.Valid {
		fmt.Fprintf(&stmt, " VALID UNTIL '%s'", role.ValidUntil.String)
	}
	
	_, err = m.dstConn.Exec(stmt.String())
	if err != nil {
		return fmt.Errorf("failed to create role %s: %w", role.Name, err)
	}
	
	log.Printf("✓ Created role: %s", role.Name)
	return nil
}

func (m *Migrator) MigrateRoles() error {
	log.Println("\n=== Migrating Roles ===")
	
	roles, err := m.GetRoles()
	if err != nil {
		return err
	}
	
	passwords, err := m.GetRolePasswords()
	if err != nil {
		return err
	}
	
	log.Printf("Found %d roles to migrate", len(roles))
	
	for _, role := range roles {
		password := passwords[role.Name]
		if err := m.CreateRole(role, password); err != nil {
			log.Printf("⨯ Failed to create role %s: %v", role.Name, err)
		}
	}
	
	return nil
}

func (m *Migrator) GetDatabases() ([]string, error) {
	query := `
		SELECT datname 
		FROM pg_database 
		WHERE datname NOT IN ('postgres', 'template0', 'template1')
		AND datistemplate = false
		ORDER BY datname;
	`
	
	rows, err := m.srcConn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query databases: %w", err)
	}
	defer rows.Close()
	
	var databases []string
	for rows.Next() {
		var dbname string
		if err := rows.Scan(&dbname); err != nil {
			return nil, fmt.Errorf("failed to scan database name: %w", err)
		}
		databases = append(databases, dbname)
	}
	
	return databases, nil
}

func (m *Migrator) GetDatabaseOwner(dbname string) (string, error) {
	query := `
		SELECT pg_catalog.pg_get_userbyid(d.datdba) as owner
		FROM pg_catalog.pg_database d
		WHERE d.datname = $1;
	`
	
	var owner string
	err := m.srcConn.QueryRow(query, dbname).Scan(&owner)
	if err != nil {
		return "", fmt.Errorf("failed to get database owner: %w", err)
	}
	
	return owner, nil
}

func (m *Migrator) DatabaseExists(dbname string) (bool, error) {
	var exists bool
	query := "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)"
	err := m.dstConn.QueryRow(query, dbname).Scan(&exists)
	return exists, err
}

func (m *Migrator) CreateDatabase(dbname, owner string) error {
	exists, err := m.DatabaseExists(dbname)
	if err != nil {
		return fmt.Errorf("failed to check if database exists: %w", err)
	}
	
	if exists {
		log.Printf("Database %s already exists, dropping...", dbname)
		_, err := m.dstConn.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS "%s"`, dbname))
		if err != nil {
			return fmt.Errorf("failed to drop database: %w", err)
		}
	}
	
	stmt := fmt.Sprintf(`CREATE DATABASE "%s"`, dbname)
	if owner != "" {
		stmt += fmt.Sprintf(` OWNER "%s"`, owner)
	}
	
	_, err = m.dstConn.Exec(stmt)
	if err != nil {
		return fmt.Errorf("failed to create database: %w", err)
	}
	
	log.Printf("✓ Created database: %s (owner: %s)", dbname, owner)
	return nil
}

func (m *Migrator) DumpDatabase(dbname, dumpFile string) error {
	log.Printf("Dumping database: %s", dbname)
	
	cmd := exec.Command("pg_dump",
		"-h", m.config.SrcHost,
		"-p", fmt.Sprintf("%d", m.config.SrcPort),
		"-U", m.config.SrcUser,
		"-F", "c", // Custom format
		"-b",      // Include large objects
		"-v",      // Verbose
		"-f", dumpFile,
		dbname,
	)
	
	cmd.Env = append(os.Environ(), fmt.Sprintf("PGPASSWORD=%s", m.config.SrcPassword))
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pg_dump failed: %w\n%s", err, string(output))
	}
	
	log.Printf("✓ Dumped %s to %s", dbname, dumpFile)
	return nil
}

func (m *Migrator) RestoreDatabase(dbname, dumpFile string) error {
	log.Printf("Restoring database: %s", dbname)
	
	cmd := exec.Command("pg_restore",
		"-h", m.config.DstHost,
		"-p", fmt.Sprintf("%d", m.config.DstPort),
		"-U", m.config.DstUser,
		"-d", dbname,
		"-v",
		"--no-owner", // Don't set ownership (we already created with correct owner)
		"--no-acl",   // Don't restore access privileges
		dumpFile,
	)
	
	cmd.Env = append(os.Environ(), fmt.Sprintf("PGPASSWORD=%s", m.config.DstPassword))
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pg_restore failed: %w\n%s", err, string(output))
	}
	
	log.Printf("✓ Restored %s", dbname)
	return nil
}

func (m *Migrator) MigrateDatabases() error {
	databases, err := m.GetDatabases()
	if err != nil {
		return err
	}
	
	log.Printf("\nFound %d databases to migrate: %s", len(databases), strings.Join(databases, ", "))
	
	// Create dump directory
	if err := os.MkdirAll(m.config.DumpDir, 0755); err != nil {
		return fmt.Errorf("failed to create dump directory: %w", err)
	}
	
	for _, dbname := range databases {
		log.Printf("\n%s", strings.Repeat("=", 60))
		log.Printf("Migrating database: %s", dbname)
		log.Printf("%s", strings.Repeat("=", 60))
		
		// Get owner
		owner, err := m.GetDatabaseOwner(dbname)
		if err != nil {
			log.Printf("⨯ Failed to get owner for %s: %v", dbname, err)
			continue
		}
		log.Printf("Database owner: %s", owner)
		
		// Create database
		if err := m.CreateDatabase(dbname, owner); err != nil {
			log.Printf("⨯ Failed to create database %s: %v", dbname, err)
			continue
		}
		
		// Dump database
		dumpFile := filepath.Join(m.config.DumpDir, fmt.Sprintf("%s.dump", dbname))
		if err := m.DumpDatabase(dbname, dumpFile); err != nil {
			log.Printf("⨯ Failed to dump %s: %v", dbname, err)
			continue
		}
		
		// Restore database
		if err := m.RestoreDatabase(dbname, dumpFile); err != nil {
			log.Printf("⨯ Failed to restore %s: %v", dbname, err)
			continue
		}
		
		// Clean up dump file
		if err := os.Remove(dumpFile); err != nil {
			log.Printf("Warning: Failed to remove dump file %s: %v", dumpFile, err)
		}
		
		log.Printf("✓ Successfully migrated %s", dbname)
	}
	
	return nil
}

func (m *Migrator) Migrate() error {
	log.Println("Starting migration process...")
	
	if err := m.Connect(); err != nil {
		return err
	}
	defer m.Close()
	
	// Migrate roles
	if err := m.MigrateRoles(); err != nil {
		return fmt.Errorf("failed to migrate roles: %w", err)
	}
	
	// Migrate databases
	if err := m.MigrateDatabases(); err != nil {
		return fmt.Errorf("failed to migrate databases: %w", err)
	}
	
	log.Println("\n" + strings.Repeat("=", 60))
	log.Println("Migration completed!")
	log.Println(strings.Repeat("=", 60))
	
	return nil
}

func main() {
	var config Config
	
	flag.StringVar(&config.SrcHost, "src-host", "", "Source server hostname")
	flag.IntVar(&config.SrcPort, "src-port", 5432, "Source server port")
	flag.StringVar(&config.SrcUser, "src-user", "", "Source server username")
	flag.StringVar(&config.SrcPassword, "src-password", "", "Source server password")
	
	flag.StringVar(&config.DstHost, "dst-host", "", "Destination server hostname")
	flag.IntVar(&config.DstPort, "dst-port", 5432, "Destination server port")
	flag.StringVar(&config.DstUser, "dst-user", "", "Destination server username")
	flag.StringVar(&config.DstPassword, "dst-password", "", "Destination server password")
	
	flag.StringVar(&config.DumpDir, "dump-dir", "/tmp/pg_migration", "Directory for temporary dump files")
	
	flag.Parse()
	
	// Validate required flags
	if config.SrcHost == "" || config.SrcUser == "" || config.SrcPassword == "" ||
		config.DstHost == "" || config.DstUser == "" || config.DstPassword == "" {
		log.Fatal("Missing required flags. Use -h for help.")
	}
	
	migrator := NewMigrator(config)
	
	if err := migrator.Migrate(); err != nil {
		log.Fatalf("Migration failed: %v", err)
	}
}
