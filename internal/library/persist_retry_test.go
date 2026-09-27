package library

import (
	"errors"
	"testing"
	"time"

	"github.com/JohanLindvall/Mediator/internal/blob"
)

type indexWriterFunc struct {
	meta func(map[string]blob.Meta) error
	item func([]blob.Item, []string) error
}

func (w indexWriterFunc) PutMetas(m map[string]blob.Meta) error {
	if w.meta != nil {
		return w.meta(m)
	}
	return nil
}

func (w indexWriterFunc) SaveItems(p []blob.Item, d []string) error {
	if w.item != nil {
		return w.item(p, d)
	}
	return nil
}

func TestMetadataFlushRetriesWithoutReplacingNewerResults(t *testing.T) {
	l := quietLib("/m")
	l.metaPending = map[string]blob.Meta{"old": {Title: "First"}, "retry": {Title: "Retry"}}
	l.flush(indexWriterFunc{meta: func(m map[string]blob.Meta) error {
		l.metaPending = map[string]blob.Meta{"old": {Title: "Newer"}}
		return errors.New("disk unavailable")
	}})
	l.flush(indexWriterFunc{meta: func(m map[string]blob.Meta) error {
		if len(m) != 2 || m["old"].Title != "Newer" || m["retry"].Title != "Retry" {
			t.Fatalf("retry wrote %+v", m)
		}
		return nil
	}})
	if len(l.metaPending) != 0 {
		t.Fatal("successful write remained pending")
	}
}

func TestFinalIndexFlushIncludesChangesDuringCommit(t *testing.T) {
	l := quietLib("/m")
	l.dirty, l.removed = map[string]struct{}{}, map[string]struct{}{}
	l.upsert("/m/first.mp3", KindAudio, 10, time.Unix(1, 0), fileKey{}, false)
	writes := 0
	l.flushFinal(indexWriterFunc{item: func(put []blob.Item, _ []string) error {
		writes++
		if writes == 1 {
			l.upsert("/m/later.mp3", KindAudio, 10, time.Unix(1, 0), fileKey{}, false)
		} else if len(put) != 1 || put[0].Path != "/m/later.mp3" {
			t.Fatalf("final commit wrote %+v", put)
		}
		return nil
	}})
	if writes != 2 {
		t.Fatalf("wrote %d times, want both commits", writes)
	}
}

func TestFinalIndexFlushIsBoundedOnFailure(t *testing.T) {
	l := quietLib("/m")
	l.metaPending = map[string]blob.Meta{"one": {Title: "Pending"}}
	writes := 0
	l.flushFinal(indexWriterFunc{meta: func(map[string]blob.Meta) error {
		writes++
		return errors.New("disk unavailable")
	}})
	if writes != finalPersistFlushRounds || len(l.metaPending) != 1 {
		t.Fatalf("writes=%d pending=%d", writes, len(l.metaPending))
	}
}
