package knowledge

import (
	"database/sql"
	"fmt"

	"gorm.io/gorm"
)

// MySQL DDL commits implicitly. Each step is idempotent; record completion only
// after all steps succeed. A connection-scoped lock serializes application starts.
func Migrate(db *gorm.DB) error {
	return db.Connection(func(conn *gorm.DB) error {
		// Connection hands us an initialized statement. Start a reusable session
		// so each query gets fresh clauses while retaining the pinned connection.
		conn = conn.Session(&gorm.Session{NewDB: true})
		var acquired sql.NullInt64
		if err := conn.Raw("SELECT GET_LOCK('gateway_knowledge_schema_v1', 15)").Scan(&acquired).Error; err != nil {
			return err
		}
		if !acquired.Valid || acquired.Int64 != 1 {
			return fmt.Errorf("knowledge migration lock unavailable")
		}
		defer conn.Exec("SELECT RELEASE_LOCK('gateway_knowledge_schema_v1')")
		if err := conn.Exec(`CREATE TABLE IF NOT EXISTS knowledge_schema_versions (version INT PRIMARY KEY, applied_at DATETIME(3) NOT NULL)`).Error; err != nil {
			return err
		}
		var count int64
		if err := conn.Table("knowledge_schema_versions").Where("version = 1").Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return migrateConversations(conn)
		}
		for _, ddl := range []string{
			`CREATE TABLE IF NOT EXISTS knowledge_libraries (
			id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, owner_id BIGINT UNSIGNED NOT NULL,
			name VARCHAR(100) NOT NULL, created_at DATETIME(3) NOT NULL,
			INDEX idx_knowledge_owner (owner_id, id)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
			`CREATE TABLE IF NOT EXISTS knowledge_sources (
			id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, library_id BIGINT UNSIGNED NOT NULL,
			title VARCHAR(200) NOT NULL, format VARCHAR(8) NOT NULL, sha256 CHAR(64) NOT NULL,
			current_version_id BIGINT UNSIGNED NOT NULL DEFAULT 0, created_at DATETIME(3) NOT NULL,
			UNIQUE KEY uq_knowledge_hash (library_id, sha256), INDEX idx_knowledge_sources (library_id, id),
			CONSTRAINT fk_knowledge_library FOREIGN KEY (library_id) REFERENCES knowledge_libraries(id)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
			`CREATE TABLE IF NOT EXISTS knowledge_source_versions (
			id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, source_id BIGINT UNSIGNED NOT NULL,
			version INT NOT NULL, body LONGTEXT NOT NULL, created_at DATETIME(3) NOT NULL,
			UNIQUE KEY uq_knowledge_version (source_id, version),
			CONSTRAINT fk_knowledge_source FOREIGN KEY (source_id) REFERENCES knowledge_sources(id)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		} {
			if err := conn.Exec(ddl).Error; err != nil {
				return err
			}
		}
		if err := conn.Exec("INSERT INTO knowledge_schema_versions(version, applied_at) VALUES (1, CURRENT_TIMESTAMP(3))").Error; err != nil {
			return err
		}
		return migrateConversations(conn)
	})
}

func migrateConversations(conn *gorm.DB) error {
	var count int64
	if err := conn.Table("knowledge_schema_versions").Where("version = 2").Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return migrateTurnEvents(conn)
	}
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS knowledge_conversations (
   id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, library_id BIGINT UNSIGNED NOT NULL,
   title VARCHAR(100) NOT NULL, created_at DATETIME(3) NOT NULL,
   INDEX idx_knowledge_conversation (library_id,id),
   CONSTRAINT fk_knowledge_conversation_library FOREIGN KEY (library_id) REFERENCES knowledge_libraries(id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS knowledge_chat_turns (
   id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, conversation_id BIGINT UNSIGNED NOT NULL,
   request_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL, input_hash CHAR(64) NOT NULL,
   question TEXT NOT NULL, prompt MEDIUMTEXT NOT NULL, sources_json TEXT NOT NULL,
   answer MEDIUMTEXT NOT NULL, status VARCHAR(16) NOT NULL,
   created_at DATETIME(3) NOT NULL, deadline_at DATETIME(3) NOT NULL,
   UNIQUE KEY uq_knowledge_chat_request (conversation_id,request_id),
   INDEX idx_knowledge_chat_turns (conversation_id,id),
   CONSTRAINT fk_knowledge_chat_conversation FOREIGN KEY (conversation_id) REFERENCES knowledge_conversations(id)
  ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	} {
		if err := conn.Exec(ddl).Error; err != nil {
			return err
		}
	}
	if err := conn.Exec("INSERT INTO knowledge_schema_versions(version, applied_at) VALUES (2, CURRENT_TIMESTAMP(3))").Error; err != nil {
		return err
	}
	return migrateTurnEvents(conn)
}

func migrateTurnEvents(conn *gorm.DB) error {
	var count int64
	if err := conn.Table("knowledge_schema_versions").Where("version = 3").Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return migrateChunks(conn)
	}
	if err := conn.Exec(`CREATE TABLE IF NOT EXISTS knowledge_turn_events (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, turn_id BIGINT UNSIGNED NOT NULL,
 payload MEDIUMTEXT NOT NULL, created_at DATETIME(3) NOT NULL,
 INDEX idx_knowledge_turn_events (turn_id,id),
 CONSTRAINT fk_knowledge_event_turn FOREIGN KEY (turn_id) REFERENCES knowledge_chat_turns(id)
 ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`).Error; err != nil {
		return err
	}
	if err := conn.Exec("INSERT INTO knowledge_schema_versions(version, applied_at) VALUES (3, CURRENT_TIMESTAMP(3))").Error; err != nil {
		return err
	}
	return migrateChunks(conn)
}

func migrateChunks(conn *gorm.DB) error {
	var count int64
	if err := conn.Table("knowledge_schema_versions").Where("version = 4").Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS knowledge_chunk_indexes (
 version_id BIGINT UNSIGNED PRIMARY KEY, rule_version VARCHAR(100) NOT NULL,
 chunk_count INT NOT NULL, updated_at DATETIME(3) NOT NULL,
 CONSTRAINT fk_knowledge_index_version FOREIGN KEY (version_id) REFERENCES knowledge_source_versions(id)
 ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		`CREATE TABLE IF NOT EXISTS knowledge_chunks (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, version_id BIGINT UNSIGNED NOT NULL,
 ordinal INT NOT NULL, start_offset INT NOT NULL, end_offset INT NOT NULL, content TEXT NOT NULL,
 UNIQUE KEY uq_knowledge_chunk (version_id,ordinal),
 CONSTRAINT fk_knowledge_chunk_version FOREIGN KEY (version_id) REFERENCES knowledge_source_versions(id)
 ) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	} {
		if err := conn.Exec(ddl).Error; err != nil {
			return err
		}
	}
	return conn.Exec("INSERT INTO knowledge_schema_versions(version, applied_at) VALUES (4, CURRENT_TIMESTAMP(3))").Error
}
