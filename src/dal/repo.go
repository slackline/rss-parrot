package dal

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"github.com/mattn/go-sqlite3"
	"rss_parrot/shared"
	"sync"
	"time"
)

const schemaVer = 1

//go:embed scripts/*
var scripts embed.FS

type IRepo interface {
	InitUpdateDb()
	GetNextId() uint64
	AddAccountIfNotExist(account *Account, privKey string) (isNew bool, err error)
	DoesAccountExist(user string) (bool, error)
	GetPrivKey(user string) (string, error)
	GetAccount(user string) (*Account, error)
	GetTootCount(user string) (uint, error)
	AddToot(accountId int, toot *Toot) error
	GetFeedLastUpdated(accountId int) (time.Time, error)
	UpdateAccountFeedTimes(accountId int, lastUpdated, nextCheckDue time.Time) error
	AddFeedPostIfNew(accountId int, post *FeedPost) (isNew bool, err error)
	GetAccountToCheck(checkDue time.Time) (*Account, error)
	GetFollowerCount(user string) (uint, error)
	GetFollowersByUser(user string) ([]*MastodonUserInfo, error)
	GetFollowersById(accountId int) ([]*MastodonUserInfo, error)
	AddFollower(user string, follower *MastodonUserInfo) error
	RemoveFollower(user, followerUserUrl string) error
	AddTootQueueItem(tqi *TootQueueItem) error
	GetTootQueueItems(aboveId, maxCount int) ([]*TootQueueItem, error)
	DeleteTootQueueItem(id int) error
}

type Repo struct {
	cfg    *shared.Config
	logger shared.ILogger
	db     *sql.DB
	muId   sync.Mutex
	nextId uint64
}

func NewRepo(cfg *shared.Config, logger shared.ILogger) IRepo {

	var err error
	var db *sql.DB

	db, err = sql.Open("sqlite3", fmt.Sprintf("file:%s??cache=shared&mode=rwc", cfg.DbFile))
	if err != nil {
		logger.Errorf("Failed to open/create DB file: %s: %v", cfg.DbFile, err)
		panic(err)
	}

	repo := Repo{
		cfg:    cfg,
		logger: logger,
		db:     db,
		nextId: uint64(time.Now().UnixNano()),
	}

	return &repo
}

func (repo *Repo) GetNextId() uint64 {
	repo.muId.Lock()
	res := repo.nextId + 1
	repo.nextId = res
	repo.muId.Unlock()
	return res
}

func (repo *Repo) InitUpdateDb() {

	dbVer := 0
	sysParamsExists := false
	var err error
	var rows *sql.Rows

	rows, err = repo.db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name='sys_params'")
	if err != nil {
		repo.logger.Errorf("Failed to check if 'sys_params' table exists: %v", err)
		panic(err)
	}
	for rows.Next() {
		sysParamsExists = true
	}
	_ = rows.Close()
	if !sysParamsExists {
		repo.logger.Printf("Database appears to be empty; current schema version is %d", schemaVer)
	} else {
		row := repo.db.QueryRow("SELECT val FROM sys_params WHERE name='schema_ver'")
		if err = row.Scan(&dbVer); err != nil {
			repo.logger.Errorf("Failed to query schema version: %v", err)
			panic(err)
		}
		repo.logger.Printf("Database is at version %d; current schema version is %d", dbVer, schemaVer)
	}
	for i := dbVer; i < schemaVer; i += 1 {
		nextVer := i + 1
		fn := fmt.Sprintf("scripts/create-%02d.sql", nextVer)
		repo.logger.Printf("Running %s", fn)
		var sqlBytes []byte
		if sqlBytes, err = scripts.ReadFile(fn); err != nil {
			repo.logger.Errorf("Failed to read init script %s: %v", fn, err)
			panic(err)
		}
		sqlStr := string(sqlBytes)
		if _, err = repo.db.Exec(sqlStr); err != nil {
			repo.logger.Errorf("Failed to execute init script %s: %v", fn, err)
			panic(err)
		}
		_, err = repo.db.Exec("UPDATE sys_params SET val=? WHERE name='schema_ver'", nextVer)
		if err != nil {
			repo.logger.Errorf("Failed to update schema_ver to %d: %v", i, err)
			panic(err)
		}
	}

	if dbVer == 0 {
		repo.mustAddBuiltInUsers()
	}

	// DBG
	_, _ = repo.AddAccountIfNotExist(&Account{Handle: "handle"}, "xyz")
	_, _ = repo.AddAccountIfNotExist(&Account{Handle: "handle"}, "xyz")
}

func (repo *Repo) mustAddBuiltInUsers() {

	idb := shared.IdBuilder{Host: repo.cfg.Host}

	_, err := repo.db.Exec(`INSERT INTO accounts
    	(created_at, user_url, handle, pubkey, privkey)
		VALUES(?, ?, ?, ?, ?)`,
		repo.cfg.Birb.Published, idb.UserUrl(repo.cfg.Birb.User),
		repo.cfg.Birb.User, repo.cfg.Birb.PubKey, repo.cfg.Birb.PrivKey)

	if err != nil {
		repo.logger.Errorf("Failed to add built-in user '%s': %v", repo.cfg.Birb.User, err)
		panic(err)
	}
}

func (repo *Repo) AddAccountIfNotExist(acct *Account, privKey string) (isNew bool, err error) {
	isNew = true
	_, err = repo.db.Exec(`INSERT INTO accounts
    	(created_at, user_url, handle, name, summary, profile_image_url, site_url, feed_url, pubkey, privkey)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		acct.CreatedAt, acct.UserUrl, acct.Handle, acct.Name, acct.Summary, acct.ProfileImageUrl,
		acct.SiteUrl, acct.FeedUrl, acct.PubKey, privKey)
	if err == nil {
		return
	}
	// MySQL: mysql.MySQLError; mysqlErr.Number == 1062
	if sqliteErr, ok := err.(sqlite3.Error); ok {
		// Duplicate key: account with this handle already exists
		if sqliteErr.Code == 19 && sqliteErr.ExtendedCode == 2067 {
			isNew = false
			_, err = repo.GetAccount(acct.Handle)
			return
		}
	}
	return
}

func (repo *Repo) DoesAccountExist(user string) (bool, error) {
	row := repo.db.QueryRow(`SELECT COUNT(*) FROM accounts WHERE handle=?`, user)
	var err error
	var count int
	if err = row.Scan(&count); err != nil {
		return false, err
	}
	return count != 0, nil
}

func (repo *Repo) GetAccount(user string) (*Account, error) {
	row := repo.db.QueryRow(
		`SELECT id, created_at, user_url, handle, name, summary, profile_image_url, site_url, feed_url,
         		feed_last_updated, next_check_due, pubkey
		FROM accounts WHERE handle=?`, user)
	var err error
	var res Account
	err = row.Scan(&res.Id, &res.CreatedAt, &res.UserUrl, &res.Handle, &res.Name, &res.Summary,
		&res.ProfileImageUrl, &res.SiteUrl, &res.FeedUrl, &res.FeedLastUpdated, &res.NextCheckDue, &res.PubKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		} else {
			return nil, err
		}
	}
	return &res, nil
}

func (repo *Repo) GetPrivKey(user string) (string, error) {
	row := repo.db.QueryRow(`SELECT privkey FROM accounts WHERE handle=?`, user)
	var err error
	var res string
	err = row.Scan(&res)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		} else {
			return "", err
		}
	}
	return res, nil
}

func (repo *Repo) SetPrivKey(user, privKey string) error {
	_, err := repo.db.Exec("UPDATE accounts SET privkey=? WHERE handle=?", privKey, user)
	if err != nil {
		return err
	}
	return nil
}

func (repo *Repo) GetTootCount(user string) (uint, error) {
	row := repo.db.QueryRow(`SELECT COUNT(*) FROM toots JOIN accounts
		ON toots.account_id=accounts.id AND accounts.handle=?`, user)
	var err error
	var count int
	if err = row.Scan(&count); err != nil {
		return 0, err
	}
	return uint(count), nil
}

func (repo *Repo) AddToot(accountId int, toot *Toot) error {
	_, err := repo.db.Exec(`INSERT INTO toots (account_id, post_guid_hash, tooted_at, status_id, content)
		VALUES(?, ?, ?, ?, ?)`,
		accountId, toot.PostGuidHash, toot.TootedAt, toot.StatusId, toot.Content)
	if err != nil {
		return err
	}
	return nil
}

func (repo *Repo) GetFollowerCount(user string) (uint, error) {
	row := repo.db.QueryRow(`SELECT COUNT(*) FROM followers JOIN accounts
		ON followers.account_id=accounts.id AND accounts.handle=?`, user)
	var err error
	var count int
	if err = row.Scan(&count); err != nil {
		return 0, err
	}
	return uint(count), nil
}

func (repo *Repo) GetFollowersByUser(user string) ([]*MastodonUserInfo, error) {
	rows, err := repo.db.Query(`SELECT followers.user_url, followers.handle, host, shared_inbox
		FROM followers JOIN accounts ON followers.account_id=accounts.id AND accounts.handle=?`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return readGetFollowers(rows)
}

func (repo *Repo) GetFollowersById(accountId int) ([]*MastodonUserInfo, error) {
	rows, err := repo.db.Query(`SELECT user_url, handle, host, shared_inbox
		FROM followers WHERE account_id=?`, accountId)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return readGetFollowers(rows)
}

func readGetFollowers(rows *sql.Rows) ([]*MastodonUserInfo, error) {
	var err error
	res := make([]*MastodonUserInfo, 0)
	for rows.Next() {
		mui := MastodonUserInfo{}
		if err = rows.Scan(&mui.UserUrl, &mui.Handle, &mui.Host, &mui.SharedInbox); err != nil {
			return nil, err
		}
		res = append(res, &mui)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return res, nil
}

func (repo *Repo) AddFollower(user string, follower *MastodonUserInfo) error {
	row := repo.db.QueryRow(`SELECT id FROM accounts WHERE handle=?`, user)
	var err error
	var accountId int
	if err = row.Scan(&accountId); err != nil {
		return err
	}
	_, err = repo.db.Exec(`INSERT INTO followers VALUES(?, ?, ?, ?, ?)`,
		accountId, follower.UserUrl, follower.Handle, follower.Host, follower.SharedInbox)
	if err != nil {
		return err
	}
	return nil
}

func (repo *Repo) RemoveFollower(user, followerUserUrl string) error {
	row := repo.db.QueryRow(`SELECT id FROM accounts WHERE handle=?`, user)
	var err error
	var accountId int
	if err = row.Scan(&accountId); err != nil {
		return err
	}
	_, err = repo.db.Exec(`DELETE FROM followers WHERE account_id=? AND user_url=?`,
		accountId, followerUserUrl)
	if err != nil {
		return err
	}
	return nil
}

func (repo *Repo) GetFeedLastUpdated(accountId int) (res time.Time, err error) {
	res = time.Time{}
	err = nil
	row := repo.db.QueryRow("SELECT feed_last_updated FROM accounts WHERE id=?", accountId)
	if err = row.Scan(&res); err != nil {
		return
	}
	return
}

func (repo *Repo) UpdateAccountFeedTimes(accountId int, lastUpdated, nextCheckDue time.Time) error {
	_, err := repo.db.Exec(`UPDATE accounts SET feed_last_updated=?, next_check_due=?
        WHERE id=?`, lastUpdated, nextCheckDue, accountId)
	return err
}

func (repo *Repo) GetAccountToCheck(checkDue time.Time) (*Account, error) {
	rows, err := repo.db.Query(`SELECT id, created_at, user_url, handle, name, summary, profile_image_url,
    	site_url, feed_url, feed_last_updated, next_check_due, pubkey
		FROM accounts WHERE next_check_due<? LIMIT 1`, checkDue)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var acct *Account = nil
	for rows.Next() {
		res := Account{}
		err = rows.Scan(&res.Id, &res.CreatedAt, &res.UserUrl, &res.Handle, &res.Name, &res.Summary,
			&res.ProfileImageUrl, &res.SiteUrl, &res.FeedUrl, &res.FeedLastUpdated, &res.NextCheckDue, &res.PubKey)
		if err = rows.Err(); err != nil {
			return nil, err
		}
		acct = &res
	}
	return acct, nil

}

func (repo *Repo) AddFeedPostIfNew(accountId int, post *FeedPost) (isNew bool, err error) {

	err = nil

	_, err = repo.db.Exec(`INSERT INTO feed_posts
    	(account_id, post_guid_hash, post_time, link, title, description)
		VALUES (?, ?, ?, ?, ?, ?)`,
		accountId, post.PostGuidHash, post.PostTime, post.Link, post.Title, post.Desription)

	if err == nil {
		isNew = true
		return
	}

	// Duplicate key: feed post for this account+guid_hash already exists
	if sqliteErr, ok := err.(*sqlite3.Error); ok {
		// Duplicate key: account with this handle already exists
		if sqliteErr.Code == 19 && sqliteErr.ExtendedCode == 2067 {
			isNew = false
			err = nil
			return
		}
	}

	return
}

func (repo *Repo) AddTootQueueItem(tqi *TootQueueItem) error {
	_, err := repo.db.Exec(`INSERT INTO toot_queue (sending_user, to_inbox, tooted_at, status_id, content)
		VALUES(?, ?, ?, ?, ?)`,
		tqi.SendingUser, tqi.ToInbox, tqi.TootedAt, tqi.StatusId, tqi.Content)
	return err
}

func (repo *Repo) GetTootQueueItems(aboveId, maxCount int) ([]*TootQueueItem, error) {
	rows, err := repo.db.Query(`SELECT id, sending_user, to_inbox, tooted_at, status_id, content
		FROM toot_queue WHERE id>? ORDER BY id ASC LIMIT ?`, aboveId, maxCount)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := make([]*TootQueueItem, 0, maxCount)
	for rows.Next() {
		tqi := TootQueueItem{}
		err = rows.Scan(&tqi.Id, &tqi.SendingUser, &tqi.ToInbox, &tqi.TootedAt, &tqi.StatusId, &tqi.Content)
		if err != nil {
			return nil, err
		}
		res = append(res, &tqi)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return res, nil
}

func (repo *Repo) DeleteTootQueueItem(id int) error {
	_, err := repo.db.Exec(`DELETE FROM toot_queue WHERE id=?`, id)
	return err
}
