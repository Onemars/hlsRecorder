package writer

import (
	"fmt"

	parser "hlsRecorder/parser"
)

type minutes struct {
	list map[int64]*minute
}

// возвращаем список всех полных минуток из сегмента
func makeMinutes(chunks, iframes []*parser.Segment) (*minutes, error) {

	if len(chunks) == 0 {
		return nil, fmt.Errorf("список chunk-сегментов пустой")
	}

	if len(iframes) == 0 {
		return nil, fmt.Errorf("список iframe-сегментов пустой")
	}

	list := make(map[int64]*minute, 0)

	for _, segment := range chunks {
		at := getMinute(segment.BeginAt)
		if _, ok := list[at]; !ok {
			list[at] = newMinute(segment.BeginAt)
		}
		list[at].chunks = append(list[at].chunks, segment)
	}

	for _, segment := range iframes {
		at := getMinute(segment.BeginAt)
		if _, ok := list[at]; !ok {
			// iframe-плейлист обгоняет/не догоняет chunks
			continue
		}
		list[at].iframes = append(list[at].iframes, segment)
	}

	// первая минута всегда в непонятном статусе,
	// поэтому мы просто ее удаляем
	delete(list, getMinute(chunks[0].BeginAt))
	if len(list) == 0 {
		return nil, fmt.Errorf("не одной целой минуты")
	}

	for _, m := range list {
		chunkFull, iframeFull := false, false
		for _, segment := range m.chunks {
			if int64(segment.EndAt) >= m.beginAt+60 {
				chunkFull = true
				break
			}
		}
		for _, segment := range m.iframes {
			if int64(segment.EndAt) >= m.beginAt+60 {
				iframeFull = true
				break
			}
		}
		m.full = chunkFull && iframeFull
	}

	return &minutes{list: list}, nil
}

func (m *minutes) last() (last *minute) {
	for _, next := range m.list {
		if last == nil || next.beginAt > last.beginAt {
			last = next
		}
	}
	return
}
