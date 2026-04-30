package graph

import (
	"strings"
	"testing"
)

// TestMongoCollectionDetection exercises the patterns we expect to
// hit in fastify-mongo / raw-mongodb-driver code. Single source file
// with a few realistic-looking patterns; we just verify the right
// collection nodes and accesses edges show up.
func TestMongoCollectionDetection(t *testing.T) {
	src := []byte(`
export async function getUserPosts(fastify, userId) {
  const users = fastify.mongo.db.collection('users');
  const user = await users.findOne({ _id: userId });
  return fastify.mongo.db.collection('posts').find({ authorId: userId }).toArray();
}

export async function getOrderWithCustomer(fastify, orderId) {
  return fastify.mongo.db.collection('orders').aggregate([
    { $match: { _id: orderId } },
    { $lookup: { from: 'customers', localField: 'customerId', foreignField: '_id', as: 'customer' } },
    { $lookup: { from: 'products', localField: 'productIds', foreignField: '_id', as: 'products' } }
  ]).toArray();
}

class OrderService {
  async list(db) {
    return db.collection('orders').find().toArray();
  }
}
`)

	fx, err := extractTS("api/orders.ts", src)
	if err != nil {
		t.Fatalf("extractTS: %v", err)
	}

	wantCollections := map[string]bool{
		"mongo:collection:users":     false,
		"mongo:collection:posts":     false,
		"mongo:collection:orders":    false,
		"mongo:collection:customers": false,
		"mongo:collection:products":  false,
	}
	for _, n := range fx.Nodes {
		if n.Kind == KindCollection {
			if _, ok := wantCollections[n.ID]; ok {
				wantCollections[n.ID] = true
			}
		}
	}
	for id, found := range wantCollections {
		if !found {
			t.Errorf("expected collection node %q not found", id)
		}
	}

	// Check accesses edges from each function to the collections it touches.
	type accessCheck struct {
		caller     string
		collection string
	}
	wantAccesses := []accessCheck{
		{"api/orders.ts::getUserPosts", "mongo:collection:users"},
		{"api/orders.ts::getUserPosts", "mongo:collection:posts"},
		{"api/orders.ts::getOrderWithCustomer", "mongo:collection:orders"},
		{"api/orders.ts::getOrderWithCustomer", "mongo:collection:customers"},
		{"api/orders.ts::getOrderWithCustomer", "mongo:collection:products"},
		{"api/orders.ts::OrderService.list", "mongo:collection:orders"},
	}

	have := map[string]bool{}
	for _, e := range fx.Edges {
		if e.Relation == RelAccesses {
			have[e.Source+"|"+e.Target] = true
		}
	}
	for _, want := range wantAccesses {
		key := want.caller + "|" + want.collection
		if !have[key] {
			t.Errorf("missing accesses edge: %s → %s", want.caller, want.collection)
		}
	}

	// Sanity: no duplicate accesses for the same (caller, collection) pair.
	if got, want := countOccurrences(fx.Edges, "api/orders.ts::getUserPosts", "mongo:collection:users", RelAccesses), 1; got != want {
		t.Errorf("duplicate accesses: got %d, want %d", got, want)
	}
}

func countOccurrences(edges []Edge, src, tgt string, rel EdgeRelation) int {
	n := 0
	for _, e := range edges {
		if e.Source == src && e.Target == tgt && e.Relation == rel {
			n++
		}
	}
	return n
}

// TestMongoIgnoresNonLiterals verifies that dynamic collection names
// (e.g. db.collection(req.params.name)) don't generate spurious
// collection nodes. Only string literals should match.
func TestMongoIgnoresNonLiterals(t *testing.T) {
	src := []byte(`
export function dynamic(db, name) {
  return db.collection(name).find();
}
export function templated(db, x) {
  return db.collection(` + "`prefix-${x}`" + `).find();
}
export function literal(db) {
  return db.collection('actual_collection').find();
}
`)
	fx, err := extractTS("api/dyn.ts", src)
	if err != nil {
		t.Fatalf("extractTS: %v", err)
	}
	gotCollections := []string{}
	for _, n := range fx.Nodes {
		if n.Kind == KindCollection {
			gotCollections = append(gotCollections, n.Label)
		}
	}
	if len(gotCollections) != 1 || gotCollections[0] != "actual_collection" {
		t.Errorf("expected only 'actual_collection', got %v", gotCollections)
	}
	_ = strings.Contains // unused import guard
}
