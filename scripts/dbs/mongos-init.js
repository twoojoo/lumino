// Health check of the mongos service: adds the two shards and shards a
// test collection (hashed key, chunks on both shards) the first time.
const shards = db.adminCommand({ listShards: 1 }).shards || [];
const want = { sa: "sa/shard-a:27017", sb: "sb/shard-b:27017" };
for (const [id, host] of Object.entries(want)) {
  if (!shards.some((s) => s._id === id)) {
    db.adminCommand({ addShard: host, name: id });
  }
}
if (!db.getSiblingDB("config").collections.findOne({ _id: "lumino_shard.events" })) {
  db.adminCommand({ enableSharding: "lumino_shard" });
  db.adminCommand({ shardCollection: "lumino_shard.events", key: { k: "hashed" }, numInitialChunks: 4 });
}
quit(db.adminCommand({ listShards: 1 }).shards.length === 2 ? 0 : 1);
