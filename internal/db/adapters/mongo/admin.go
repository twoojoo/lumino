package mongo

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/twoojoo/lumino/internal/db"
)

// stageTemplates are the aggregation stages of the pipeline builder.
var stageTemplates = [][2]string{
	{"$match", "{}"},
	{"$project", "{field: 1}"},
	{"$group", `{_id: "$field", count: {$sum: 1}}`},
	{"$sort", "{field: 1}"},
	{"$limit", "10"},
	{"$skip", "0"},
	{"$unwind", `"$field"`},
	{"$lookup", `{from: "collection", localField: "field", foreignField: "_id", as: "joined"}`},
	{"$addFields", `{newField: "$field"}`},
	{"$set", `{newField: "$field"}`},
	{"$unset", `"field"`},
	{"$replaceRoot", `{newRoot: "$field"}`},
	{"$count", `"count"`},
	{"$sortByCount", `"$field"`},
	{"$bucket", `{groupBy: "$field", boundaries: [0, 100, 1000], default: "other"}`},
	{"$facet", `{byField: [{$sortByCount: "$field"}]}`},
	{"$sample", "{size: 10}"},
	{"$graphLookup", `{from: "collection", startWith: "$field", connectFromField: "field", connectToField: "_id", as: "graph"}`},
	{"$out", `"collection"`},
	{"$merge", `{into: "collection"}`},
}

func (*language) StageTemplates() [][2]string { return stageTemplates }

// Aggregate builds db.coll.aggregate([stages…, {$limit: n}]).
func (*language) Aggregate(ref db.ObjectRef, stages []string, limit int) (string, error) {
	parts := make([]string, 0, len(stages)+1)
	for i, s := range stages {
		v, err := parseValue(s)
		if err != nil {
			return "", fmt.Errorf("stage %d: %w", i+1, err)
		}
		d, ok := v.(bson.D)
		if !ok || len(d) != 1 || !strings.HasPrefix(d[0].Key, "$") {
			return "", fmt.Errorf("stage %d: a stage is a document with one operator, like {$match: {…}}", i+1)
		}
		parts = append(parts, strings.TrimSpace(s))
	}
	if limit > 0 {
		parts = append(parts, "{$limit: "+strconv.Itoa(limit)+"}")
	}
	return collExpr(ref) + ".aggregate([" + strings.Join(parts, ", ") + "])", nil
}

// docArg validates a document argument.
func docArg(name, s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "{}", nil
	}
	if _, err := parseDocument(s); err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return s, nil
}

// rolesArg validates a roles list: ["readWrite", {role: "read", db: "x"}].
func rolesArg(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "[]", nil
	}
	v, err := parseValue(s)
	if err != nil {
		return "", fmt.Errorf("roles: %w", err)
	}
	if _, ok := v.(bson.A); !ok {
		return "", errors.New(`roles: an array is needed, like ["readWrite", {role: "read", db: "reports"}]`)
	}
	return s, nil
}

func dbExpr(name string) string {
	if name == "" {
		return "db"
	}
	return "db.getSiblingDB(" + strconv.Quote(name) + ")"
}

func (*language) AdminStatement(op string, ref db.ObjectRef, args ...string) (string, error) {
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	coll := collExpr(ref)
	if ref.Schema != "" && ref.Name != "" {
		coll = dbExpr(ref.Schema) + ".getCollection(" + strconv.Quote(ref.Name) + ")"
	}
	switch op {
	case "createIndex":
		keys, err := docArg("keys", arg(0))
		if err != nil {
			return "", err
		}
		if keys == "{}" {
			return "", errors.New("keys: name at least one field, like {email: 1}")
		}
		opts, err := docArg("options", arg(1))
		if err != nil {
			return "", err
		}
		if opts == "{}" {
			return coll + ".createIndex(" + keys + ")", nil
		}
		return coll + ".createIndex(" + keys + ", " + opts + ")", nil
	case "dropIndex":
		if strings.TrimSpace(arg(0)) == "" {
			return "", errors.New("index name missing")
		}
		return coll + ".dropIndex(" + strconv.Quote(arg(0)) + ")", nil
	case "setValidator":
		v, err := docArg("validator", arg(0))
		if err != nil {
			return "", err
		}
		level, action := arg(1), arg(2)
		if level == "" {
			level = "strict"
		}
		if action == "" {
			action = "error"
		}
		switch level {
		case "off", "strict", "moderate":
		default:
			return "", fmt.Errorf("validation level %q: off, strict or moderate", level)
		}
		switch action {
		case "error", "warn":
		default:
			return "", fmt.Errorf("validation action %q: error or warn", action)
		}
		return fmt.Sprintf("%s.runCommand({collMod: %s, validator: %s, validationLevel: %q, validationAction: %q})",
			dbExpr(ref.Schema), strconv.Quote(ref.Name), v, level, action), nil
	case "listUsers":
		return dbExpr(ref.Schema) + ".runCommand({usersInfo: 1})", nil
	case "createUser":
		user := strings.TrimSpace(arg(0))
		if user == "" {
			return "", errors.New("user name missing")
		}
		if arg(1) == "" {
			return "", errors.New("password missing")
		}
		roles, err := rolesArg(arg(2))
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s.runCommand({createUser: %s, pwd: %s, roles: %s})", dbExpr(ref.Schema), strconv.Quote(user), strconv.Quote(arg(1)), roles), nil
	case "dropUser":
		if strings.TrimSpace(arg(0)) == "" {
			return "", errors.New("user name missing")
		}
		return fmt.Sprintf("%s.runCommand({dropUser: %s})", dbExpr(ref.Schema), strconv.Quote(arg(0))), nil
	case "grantRoles", "revokeRoles":
		roles, err := rolesArg(arg(1))
		if err != nil {
			return "", err
		}
		cmd := "grantRolesToUser"
		if op == "revokeRoles" {
			cmd = "revokeRolesFromUser"
		}
		return fmt.Sprintf("%s.runCommand({%s: %s, roles: %s})", dbExpr(ref.Schema), cmd, strconv.Quote(arg(0)), roles), nil
	case "killOp":
		if _, err := parseValue(arg(0)); err != nil {
			return "", fmt.Errorf("operation id: %w", err)
		}
		return "db.killOp(" + arg(0) + ")", nil
	}
	return "", fmt.Errorf("unknown management statement %q", op)
}
