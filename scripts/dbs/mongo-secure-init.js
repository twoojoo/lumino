// Health check of the mongo-secure service (keyFile, TLS, authentication):
// initiates the replica set and creates the users through the localhost
// exception the first time, then checks the node is primary.
const admin = db.getSiblingDB("admin");
let authed = false;
try {
  admin.auth("lumino", "lumino");
  authed = true;
} catch (e) {}
if (!authed) {
  try {
    rs.initiate({ _id: "rs1", members: [{ _id: 0, host: "localhost:27017" }] });
  } catch (e) {} // already initiated
  if (!db.hello().isWritablePrimary) quit(1);
  admin.createUser({ user: "lumino", pwd: "lumino", roles: ["root"] });
  admin.auth("lumino", "lumino");
  // X.509 user: the subject of .dev/certs/client.crt
  db.getSiblingDB("$external").runCommand({
    createUser: "CN=lumino",
    roles: [{ role: "readWriteAnyDatabase", db: "admin" }, { role: "clusterMonitor", db: "admin" }],
  });
}
quit(db.hello().isWritablePrimary ? 0 : 1);
