package writer

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"syscall"

	hedx "hlsRecorder/index/hedx"
	keys "hlsRecorder/keys"
)

func round(f float64) float64 {
	return float64(int64(f*100)) / 100
}

// открываем индексный файл и проверяем последние индексы
// и записываем все сегменты что больше чем эти последние индексы
func (m *minute) writePartical(indexDir, storageDir, resource string, vmx *keys.VMX) (error, int64, int64, float64) {

	chunkWrited, iframeWrited, last := int64(0), int64(0), float64(0)

	if len(m.iframes) == 0 || len(m.chunks) == 0 {
		return fmt.Errorf("пустая минутка"), chunkWrited, iframeWrited, last
	}

	chunkFile, iframeFile, indexFile := m.getPath(storageDir, `.ets`), m.getPath(storageDir, `.ets.ifr`), m.getPath(indexDir, `.ets.hedx`)

	if err := os.MkdirAll(filepath.Dir(chunkFile), 0755); err != nil {
		return err, chunkWrited, iframeWrited, last
	}
	if err := os.MkdirAll(filepath.Dir(indexFile), 0755); err != nil {
		return err, chunkWrited, iframeWrited, last
	}

	chunkFD, err := os.OpenFile(chunkFile, os.O_RDWR|os.O_CREATE|syscall.O_APPEND, 0644)
	if err != nil {
		return err, chunkWrited, iframeWrited, last
	}
	defer chunkFD.Close()

	iframeFD, err := os.OpenFile(iframeFile, os.O_RDWR|os.O_CREATE|syscall.O_APPEND, 0644)
	if err != nil {
		return err, chunkWrited, iframeWrited, last
	}
	defer iframeFD.Close()

	indexFD, err := os.OpenFile(indexFile, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return err, chunkWrited, iframeWrited, last
	}
	defer indexFD.Close()
	if _, err := indexFD.Seek(0, 0); err != nil {
		return err, chunkWrited, iframeWrited, last
	}

	// создаем заготовки для хидеров
	headersRange, headers := make(map[string]string, 1), make(map[string]string, 0)

	// параметры ключа
	var keyData []byte
	keyPosition := m.beginAt

	// в LastIndex мы передвигаемся до конца дескриптора
	lastChunk, chunkKey, lastIFrame, iframeKey := hedx.LastIndexesWithKeys(indexFD)

	// разбираемся с ключами
	// если мы только открыли index и там нет ключей
	if chunkKey.Type == hedx.TypeInvalid && iframeKey.Type == hedx.TypeInvalid {
		// получаем их
		newKeyData, newKeyPosition, err := vmx.GetKeyPosition(resource, keys.ResourceTypeDTV, m.beginAt)
		if err != nil {
			return err, chunkWrited, iframeWrited, last
		}
		keyData, keyPosition = newKeyData, newKeyPosition
		chunkKey.ChunkKey(0, keyPosition, m.chunks[0].BeginAt-float64(m.beginAt))
		if err := chunkKey.Write(indexFD); err != nil {
			return err, chunkWrited, iframeWrited, last
		}
		iframeKey.IFrameKey(0, keyPosition, m.iframes[0].BeginAt-float64(m.beginAt))
		if err := iframeKey.Write(indexFD); err != nil {
			return err, chunkWrited, iframeWrited, last
		}
	} else {
		// если в chunkKey и в iframeKey лежат какие-то данные, попробуем получить keyData
		newKeyData, newKeyPosition, err := vmx.GetKeyPosition(resource, keys.ResourceTypeDTV, int64(chunkKey.SizeBytes))
		if err != nil {
			return err, chunkWrited, iframeWrited, last
		}
		if uint64(newKeyPosition) != chunkKey.SizeBytes || chunkKey.SizeBytes != iframeKey.SizeBytes {
			// нужно записать новые ключи
			chunkKey.ChunkKey(0, newKeyPosition, m.chunks[0].BeginAt-float64(m.beginAt))
			if err := chunkKey.Write(indexFD); err != nil {
				return err, chunkWrited, iframeWrited, last
			}
			iframeKey.IFrameKey(0, newKeyPosition, m.iframes[0].BeginAt-float64(m.beginAt))
			if err := iframeKey.Write(indexFD); err != nil {
				return err, chunkWrited, iframeWrited, last
			}
		} else {
			keyData = newKeyData
		}
	}

	stat, err := chunkFD.Stat()
	if err != nil {
		return err, chunkWrited, iframeWrited, last
	}
	chunkOffset := stat.Size()

	chunkLength := len(m.chunks)
	// проверяем что дописать по chunks
	for i, s := range m.chunks {
		if round(s.BeginAt-float64(m.beginAt)) > round(lastChunk.TimeStampInSec()) {
			index := &hedx.Index{}
			if s.ByteRange != nil {
				headersRange["Range"] = fmt.Sprintf(
					"bytes=%d-%d", s.ByteRange.Offset, s.ByteRange.Offset+s.ByteRange.Length-1)
				headers = headersRange
			}
			http, err := fetchURLWithRetry(s.URL, headers, 3)
			if err != nil {
				return err, chunkWrited, iframeWrited, last
			}
			//writeSize, err := io.Copy(chunkFD, http)
			writeSize, err := vmx.Crypto(http, chunkFD, keyPosition, keyData)
			if err != nil {
				log.Printf("[ERROR] при шифровании %s: %s\n", s.ToString(), err.Error())
				return err, chunkWrited, iframeWrited, last
			}
			if m.full && i == chunkLength-1 {
				// записываем CHUNK OEF
				index.ChunkEOF(chunkOffset+writeSize, 0, s.BeginAt-float64(m.beginAt))
			} else {
				// обычный CHUNK
				index.Chunk(chunkOffset, writeSize, s.BeginAt-float64(m.beginAt))
			}
			if err := index.Write(indexFD); err != nil {
				return err, chunkWrited, iframeWrited, last
			}
			if err := indexFD.Sync(); err != nil {
				return err, chunkWrited, iframeWrited, last
			}
			headers = nil
			chunkOffset = chunkOffset + writeSize
			http.Close()
			chunkWrited++
			if s.EndAt > last {
				last = s.EndAt
			}
		}
	}

	stat, err = iframeFD.Stat()
	if err != nil {
		return err, chunkWrited, iframeWrited, last
	}
	iframeOffset := stat.Size()

	iframeLength := len(m.iframes)
	// проверяем что дописать по iframes
	for i, s := range m.iframes {
		if round(s.BeginAt-float64(m.beginAt)) > round(lastIFrame.TimeStampInSec()) {
			index := &hedx.Index{}
			if s.ByteRange != nil {
				headersRange["Range"] = fmt.Sprintf(
					"bytes=%d-%d", s.ByteRange.Offset, s.ByteRange.Offset+s.ByteRange.Length-1)
				headers = headersRange
			}
			http, err := fetchURLWithRetry(s.URL, headers, 3)
			if err != nil {
				return err, chunkWrited, iframeWrited, last
			}
			//writeSize, err := io.Copy(iframeFD, http)
			writeSize, err := vmx.Crypto(http, iframeFD, keyPosition, keyData)
			if err != nil {
				log.Printf("[ERROR] при шифровании %s: %s\n", s.ToString(), err.Error())
				return err, chunkWrited, iframeWrited, last
			}
			if m.full && i == iframeLength-1 {
				// записываем IFRAME OEF
				index.IFrameEOF(iframeOffset+writeSize, 0, s.BeginAt-float64(m.beginAt))
			} else {
				// обычный IFRAME
				index.IFrame(iframeOffset, writeSize, s.BeginAt-float64(m.beginAt))
			}
			if err := index.Write(indexFD); err != nil {
				return err, chunkWrited, iframeWrited, last
			}
			if err := indexFD.Sync(); err != nil {
				return err, chunkWrited, iframeWrited, last
			}
			headers = nil
			iframeOffset = iframeOffset + writeSize
			http.Close()
			iframeWrited++
			if s.EndAt > last {
				last = s.EndAt
			}
		}
	}

	// синкаем все
	if err := chunkFD.Sync(); err != nil {
		return err, chunkWrited, iframeWrited, last
	}
	if err := iframeFD.Sync(); err != nil {
		return err, chunkWrited, iframeWrited, last
	}
	if err := indexFD.Sync(); err != nil {
		return err, chunkWrited, iframeWrited, last
	}

	return nil, chunkWrited, iframeWrited, last

}
