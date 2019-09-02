package cronsun

import (
	"github.com/xiao5-neradigm/cronsun/db"
)

var (
	mgoDB *db.Mdb
)

func GetDb() *db.Mdb {
	return mgoDB
}
