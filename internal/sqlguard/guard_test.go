package sqlguard

import (
	"strings"
	"testing"
)

func TestValidate_Positive(t *testing.T) {
	cases := []struct {
		name string
		q    string
	}{
		{"simple SELECT", "SELECT 1"},
		{"SELECT with FROM", "SELECT id FROM users"},
		{"JOIN", "SELECT u.id FROM users u JOIN orders o ON o.user_id = u.id"},
		{"CTE", "WITH t AS (SELECT 1 AS n) SELECT n FROM t"},
		{"CTE multiple", "WITH a AS (SELECT 1), b AS (SELECT 2) SELECT * FROM a, b"},
		{"UNION ALL", "SELECT id FROM t1 UNION ALL SELECT id FROM t2"},
		{"UNION", "SELECT id FROM t1 UNION SELECT id FROM t2"},
		{"INTERSECT", "SELECT id FROM t1 INTERSECT SELECT id FROM t2"},
		{"EXCEPT", "SELECT id FROM t1 EXCEPT SELECT id FROM t2"},
		{"SELECT *", "SELECT * FROM users WHERE id < 10"},
		{"comment then SELECT", "/* note */ SELECT 1"},
		{"comment with INSERT inside string", `SELECT 'INSERT INTO bad' AS msg`},
		{"window function", "SELECT id, ROW_NUMBER() OVER (PARTITION BY x ORDER BY y) FROM t"},
		{"subquery in SELECT", "SELECT (SELECT COUNT(*) FROM orders) AS n"},
		{"GROUP BY HAVING", "SELECT user_id, COUNT(*) FROM orders GROUP BY user_id HAVING COUNT(*) > 5"},
		{"trailing semicolon", "SELECT 1;"},
		{"trailing semicolon with whitespace", "SELECT 1  ;  "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(tc.q); err != nil {
				t.Errorf("Validate(%q) = %v, want nil", tc.q, err)
			}
		})
	}
}

func TestValidate_Negative(t *testing.T) {
	cases := []struct {
		name    string
		q       string
		errFrag string
	}{
		{"empty", "", "empty"},
		{"whitespace", "   ", "empty"},
		{"comment only", "/* just a comment */", "empty"},
		{"DROP TABLE", "DROP TABLE users", "SELECT"},
		{"INSERT", "INSERT INTO users (id) VALUES (1)", "SELECT"},
		{"UPDATE", "UPDATE users SET name='x'", "SELECT"},
		{"DELETE", "DELETE FROM users", "SELECT"},
		{"TRUNCATE", "TRUNCATE TABLE users", "SELECT"},
		{"ALTER", "ALTER TABLE users ADD COLUMN x INT", "SELECT"},
		{"CREATE", "CREATE TABLE x (id INT)", "SELECT"},
		{"RENAME", "RENAME TABLE a TO b", "SELECT"},
		{"GRANT", "GRANT SELECT ON *.* TO 'x'", "SELECT"},
		{"REVOKE", "REVOKE SELECT ON *.* FROM 'x'", "SELECT"},
		{"CALL", "CALL my_proc()", "SELECT"},
		{"SET", "SET sql_safe_updates = 1", "SELECT"},
		{"LOAD", "LOAD DATA INFILE 'x' INTO TABLE t", "SELECT"},
		{"LOCK", "LOCK TABLES users WRITE", "SELECT"},
		{"UNLOCK", "UNLOCK TABLES", "SELECT"},
		{"USE", "USE another_db", "SELECT"},
		{"REPLACE", "REPLACE INTO users SET id=1", "SELECT"},
		{"SHOW TABLES", "SHOW TABLES", "SELECT"},
		{"multi-statement", "SELECT 1; DROP TABLE x", "ровно один"},
		{"multi-statement 2 selects", "SELECT 1; SELECT 2", "ровно один"},
		{"SELECT INTO OUTFILE", "SELECT * FROM t INTO OUTFILE '/tmp/x.csv'", "INTO"},
		// Эти два формы TiDB-парсер отклоняет как syntax error — это тоже валидно,
		// главное, чтобы Validate вернул ошибку.
		{"SELECT INTO DUMPFILE", "SELECT * FROM t INTO DUMPFILE '/tmp/x'", ""},
		{"SELECT INTO var", "SELECT id INTO @v FROM t", ""},
		{"SELECT FOR UPDATE", "SELECT * FROM t FOR UPDATE", "FOR UPDATE"},
		{"SELECT LOCK IN SHARE MODE", "SELECT * FROM t LOCK IN SHARE MODE", "FOR UPDATE"},
		{"UNION with FOR UPDATE", "SELECT 1 FROM t UNION SELECT 1 FROM t2 FOR UPDATE", "FOR UPDATE"},
		{"comment-evading DROP", "/* SELECT */ DROP TABLE x", "SELECT"},
		{"PREPARE", "PREPARE s FROM 'SELECT 1'", "SELECT"},
		{"EXECUTE", "EXECUTE s", "SELECT"},
		{"DEALLOCATE", "DEALLOCATE PREPARE s", "SELECT"},
		{"syntax error", "SELECT FROM WHERE", "syntax"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.q)
			if err == nil {
				t.Fatalf("Validate(%q) = nil, want error", tc.q)
			}
			// Пустой errFrag означает «любая ошибка подходит».
			if tc.errFrag != "" && !strings.Contains(err.Error(), tc.errFrag) {
				t.Errorf("error %q should contain %q", err.Error(), tc.errFrag)
			}
		})
	}
}
