package util

type FilterInfo struct {
}

func NetlinkFilterGet(ifn string, fd int) ([]FilterInfo, error) {
	return nil, nil
}

func NetlinkFilterOpen() (int, error) {
	return -1, nil
}

func NetlinkFilterClose(fd int) {
}
