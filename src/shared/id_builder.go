package shared

import (
	"fmt"
	"net/url"
	"strconv"
)

const ActivityPublic = "https://www.w3.org/ns/activitystreams#Public"

func GetHostName(userUrl string) (string, error) {
	var parsedUrl *url.URL
	var urlError error
	parsedUrl, urlError = url.Parse(userUrl)
	if urlError != nil {
		return "", fmt.Errorf("Failed to parse user URL '%s': %v", userUrl, urlError)
	}
	return parsedUrl.Hostname(), nil
}

func MakeFullMoniker(hostName, handle string) string {
	return "@" + handle + "@" + hostName
}

type IdBuilder struct {
	Host string
}

func (idb *IdBuilder) SharedInbox() string {
	return fmt.Sprintf("https://%s/inbox", idb.Host)
}

func (idb *IdBuilder) UserProfile(user string) string {
	return fmt.Sprintf("https://%s/@%s", idb.Host, user)
}

func (idb *IdBuilder) UserUrl(user string) string {
	return fmt.Sprintf("https://%s/u/%s", idb.Host, user)
}

func (idb *IdBuilder) UserKeyId(user string) string {
	return fmt.Sprintf("https://%s/u/%s#main-key", idb.Host, user)
}

func (idb *IdBuilder) UserInbox(user string) string {
	return fmt.Sprintf("https://%s/u/%s/inbox", idb.Host, user)
}

func (idb *IdBuilder) UserOutbox(user string) string {
	return fmt.Sprintf("https://%s/u/%s/outbox", idb.Host, user)
}

func (idb *IdBuilder) UserFollowing(user string) string {
	return fmt.Sprintf("https://%s/u/%s/following", idb.Host, user)
}

func (idb *IdBuilder) UserFollowers(user string) string {
	return fmt.Sprintf("https://%s/u/%s/followers", idb.Host, user)
}

func (idb *IdBuilder) UserStatus(user string, id uint64) string {
	idStr := strconv.FormatUint(id, 10)
	return fmt.Sprintf("https://%s/u/%s/status/%s", idb.Host, user, idStr)
}

func (idb *IdBuilder) UserStatusActivity(user string, id uint64) string {
	idStr := strconv.FormatUint(id, 10)
	return fmt.Sprintf("https://%s/u/%s/status/%s/activity", idb.Host, user, idStr)
}