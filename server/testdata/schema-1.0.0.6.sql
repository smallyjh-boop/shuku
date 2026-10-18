-- 1.0.0.6 版的数据库结构（升级测试用，不要改）
CREATE TABLE IF NOT EXISTS libraries (
	id INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	path TEXT NOT NULL,
	kind TEXT NOT NULL DEFAULT 'book',      -- book | comic
	template TEXT NOT NULL DEFAULT '',
	sort INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS books (
	id INTEGER PRIMARY KEY,
	title TEXT NOT NULL DEFAULT '',
	orig_title TEXT NOT NULL DEFAULT '',
	author TEXT NOT NULL DEFAULT '',
	translator TEXT NOT NULL DEFAULT '',
	series TEXT NOT NULL DEFAULT '',
	series_index REAL NOT NULL DEFAULT 0,
	year INTEGER NOT NULL DEFAULT 0,
	category TEXT NOT NULL DEFAULT '',
	tags TEXT NOT NULL DEFAULT '[]',
	description TEXT NOT NULL DEFAULT '',
	isbn TEXT NOT NULL DEFAULT '',
	language TEXT NOT NULL DEFAULT '',
	publisher TEXT NOT NULL DEFAULT '',
	kind TEXT NOT NULL DEFAULT 'book',      -- book | comic
	direction TEXT NOT NULL DEFAULT '',     -- rtl 表示日漫从右到左
	status TEXT NOT NULL DEFAULT 'ok',      -- ok | review
	confidence REAL NOT NULL DEFAULT 0,
	source TEXT NOT NULL DEFAULT '',        -- 识别来源说明
	orig_name TEXT NOT NULL DEFAULT '',     -- 入库时的原始文件名
	cover TEXT NOT NULL DEFAULT '',
	search_key TEXT NOT NULL DEFAULT '',
	sort_key TEXT NOT NULL DEFAULT '',
	author_key TEXT NOT NULL DEFAULT '',
	read_state TEXT NOT NULL DEFAULT 'unread', -- unread | reading | done
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS files (
	id INTEGER PRIMARY KEY,
	book_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
	library_id INTEGER NOT NULL DEFAULT 0,
	path TEXT NOT NULL UNIQUE,
	format TEXT NOT NULL,
	size INTEGER NOT NULL DEFAULT 0,
	hash TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_files_book ON files(book_id);
CREATE INDEX IF NOT EXISTS idx_files_hash ON files(hash);
CREATE INDEX IF NOT EXISTS idx_books_status ON books(status);
CREATE INDEX IF NOT EXISTS idx_books_created ON books(created_at);
CREATE TABLE IF NOT EXISTS progress (
	book_id INTEGER PRIMARY KEY REFERENCES books(id) ON DELETE CASCADE,
	file_id INTEGER NOT NULL DEFAULT 0,
	location TEXT NOT NULL DEFAULT '',
	percent REAL NOT NULL DEFAULT 0,
	device TEXT NOT NULL DEFAULT '',
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS bookmarks (
	id INTEGER PRIMARY KEY,
	book_id INTEGER NOT NULL REFERENCES books(id) ON DELETE CASCADE,
	location TEXT NOT NULL,
	label TEXT NOT NULL DEFAULT '',
	text TEXT NOT NULL DEFAULT '',
	note TEXT NOT NULL DEFAULT '',
	color TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS ops (
	id INTEGER PRIMARY KEY,
	batch TEXT NOT NULL,
	label TEXT NOT NULL DEFAULT '',
	from_path TEXT NOT NULL,
	to_path TEXT NOT NULL,
	file_id INTEGER NOT NULL DEFAULT 0,
	undone INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_ops_batch ON ops(batch);
CREATE TABLE IF NOT EXISTS events (
	id INTEGER PRIMARY KEY,
	level TEXT NOT NULL,      -- info | warn | error
	message TEXT NOT NULL,
	book_id INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS sessions (token TEXT PRIMARY KEY, device TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, last_seen INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS ignored_suggestions (key TEXT PRIMARY KEY, created_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS dict_ai (key TEXT PRIMARY KEY, answer TEXT NOT NULL, created_at INTEGER NOT NULL);

-- 1.0.0.6 的 migrate()
ALTER TABLE books ADD COLUMN finished_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE books ADD COLUMN enriched_at INTEGER NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS reading_log (day TEXT NOT NULL, book_id INTEGER NOT NULL, seconds INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(day, book_id));
CREATE INDEX IF NOT EXISTS idx_reading_log_book ON reading_log(book_id);
-- 1.0.0.6 的全文索引（在主库里）
CREATE TABLE IF NOT EXISTS ft_chunks (id INTEGER PRIMARY KEY, book_id INTEGER NOT NULL, file_id INTEGER NOT NULL, sec INTEGER NOT NULL, part INTEGER NOT NULL DEFAULT 0, label TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS idx_ft_chunks_file ON ft_chunks(file_id);
CREATE TABLE IF NOT EXISTS ft_files (file_id INTEGER PRIMARY KEY, hash TEXT NOT NULL, chunks INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL DEFAULT 'ok', indexed_at INTEGER NOT NULL);
CREATE VIRTUAL TABLE IF NOT EXISTS ft USING fts5(body, content='', contentless_delete=1, tokenize='unicode61 remove_diacritics 0');
