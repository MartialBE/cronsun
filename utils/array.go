package utils

func Isset(array []string, key int) (value string) {
	value = ""
	for k, v := range array {
		if k == key {
			value = v
			break
		}
	}
	return
}

func Implode(glue string, pieces []string) (str string) {
	str = ""
	for _, v := range pieces {
		str += glue + v
	}

	return
}
