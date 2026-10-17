module shuku

go 1.24

require (
	github.com/mattn/go-sqlite3 v1.14.52
	github.com/mozillazg/go-pinyin v0.21.0
	github.com/nwaples/rardecode/v2 v2.4.5
	golang.org/x/crypto v0.31.0
	golang.org/x/image v0.23.0
	golang.org/x/text v0.21.0
)

replace (
	golang.org/x/crypto => github.com/golang/crypto v0.31.0
	golang.org/x/image => github.com/golang/image v0.23.0
	golang.org/x/text => github.com/golang/text v0.21.0
)
