package cronsun

import (
	"github.com/MartialBE/cronsun/db"
)

var (
	mgoDB *db.Mdb
)

func GetDb() *db.Mdb {
	return mgoDB
}
