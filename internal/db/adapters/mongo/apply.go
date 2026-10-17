package mongo

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/twoojoo/lumino/internal/db"
)

var _ db.ChangeApplier = (*session)(nil)

// keyFilter is the filter identifying the document of a change.
func keyFilter(key []db.ColValue) (bson.D, error) {
	if len(key) == 0 {
		return nil, errors.New("no key: the document can't be identified")
	}
	f := bson.D{}
	for _, cv := range key {
		v, err := toBSON(cv.Value, cv.Kind, nil)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", cv.Column, err)
		}
		f = append(f, bson.E{Key: cv.Column, Value: v})
	}
	return f, nil
}

// ApplyChanges applies grid edits: one insertOne / updateOne ($set) /
// deleteOne per change, in a transaction when the server has them.
// Updated values keep the BSON type of the stored value (an int stays an
// int, a long a long). Updates and deletes must match exactly one
// document.
func (s *session) ApplyChanges(ctx context.Context, ref db.ObjectRef, changes []db.Change) ([]db.ApplyResult, error) {
	s.closeCursor()
	if s.readOnly {
		return nil, errors.New("read-only session: changes are refused")
	}
	dbName := ref.Schema
	if dbName == "" {
		dbName = s.database
	}
	coll := s.c.cl.Database(dbName).Collection(ref.Name)
	own := false
	if s.c.caps.Transactions && !s.TxActive() {
		if err := s.Begin(ctx); err != nil {
			return nil, err
		}
		own = true
	}
	inTx := s.TxActive()
	s.mu.Lock()
	ms := s.ms
	s.mu.Unlock()
	octx := ctx
	if ms != nil {
		octx = mongo.NewSessionContext(ctx, ms)
	}
	var out []db.ApplyResult
	fail := func(err error) ([]db.ApplyResult, error) {
		switch {
		case own:
			_ = s.Rollback(ctx)
		case inTx:
			err = fmt.Errorf("%w (earlier documents of this batch are still in your open transaction)", err)
		default:
			return out, err // no transactions: the earlier changes stay applied
		}
		return nil, err
	}
	lang := s.c.lang
	for i, ch := range changes {
		text := lang.RenderChange(ref, ch)
		var n int64
		switch ch.Op {
		case db.OpInsert:
			doc := bson.D{}
			for _, cv := range ch.Values {
				if cv.Value == nil {
					continue // an empty cell is a missing field
				}
				v, err := toBSON(cv.Value, cv.Kind, nil)
				if err != nil {
					return fail(fmt.Errorf("insert row %d, %s: %w", i+1, cv.Column, err))
				}
				doc = append(doc, bson.E{Key: cv.Column, Value: v})
			}
			if _, has := field(doc, "_id"); !has {
				doc = append(bson.D{{Key: "_id", Value: bson.NewObjectID()}}, doc...)
			}
			if _, err := coll.InsertOne(octx, doc); err != nil {
				return fail(fmt.Errorf("insert row %d: %w", i+1, mapError(err)))
			}
			n = 1
		case db.OpUpdate:
			f, err := keyFilter(ch.Key)
			if err != nil {
				return fail(err)
			}
			// the stored values give the BSON types to keep
			proj := bson.D{}
			for _, cv := range ch.Values {
				proj = append(proj, bson.E{Key: cv.Column, Value: 1})
			}
			var cur bson.D
			err = coll.FindOne(octx, f, options.FindOne().SetProjection(proj)).Decode(&cur)
			if errors.Is(err, mongo.ErrNoDocuments) {
				return fail(fmt.Errorf("update row %d: no document matched (it was changed or deleted by someone else); nothing was saved", i+1))
			}
			if err != nil {
				return fail(fmt.Errorf("update row %d: %w", i+1, mapError(err)))
			}
			set := bson.D{}
			for _, cv := range ch.Values {
				like, _ := field(cur, cv.Column)
				v, err := toBSON(cv.Value, cv.Kind, like)
				if err != nil {
					return fail(fmt.Errorf("update row %d, %s: %w", i+1, cv.Column, err))
				}
				set = append(set, bson.E{Key: cv.Column, Value: v})
			}
			r, err := coll.UpdateOne(octx, f, bson.D{{Key: "$set", Value: set}})
			if err != nil {
				return fail(fmt.Errorf("update row %d: %w", i+1, mapError(err)))
			}
			if r.MatchedCount != 1 {
				return fail(fmt.Errorf("update row %d: %d documents matched the key; nothing was saved", i+1, r.MatchedCount))
			}
			n = r.ModifiedCount
		case db.OpDelete:
			f, err := keyFilter(ch.Key)
			if err != nil {
				return fail(err)
			}
			r, err := coll.DeleteOne(octx, f)
			if err != nil {
				return fail(fmt.Errorf("delete row %d: %w", i+1, mapError(err)))
			}
			if r.DeletedCount != 1 {
				return fail(fmt.Errorf("delete row %d: no document matched (it was changed or deleted by someone else); nothing was saved", i+1))
			}
			n = 1
		}
		out = append(out, db.ApplyResult{Change: i, Affected: n, SQL: text})
	}
	if own {
		if err := s.Commit(ctx); err != nil {
			return nil, err
		}
	}
	return out, nil
}
