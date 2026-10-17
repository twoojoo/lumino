// MongoDB conformance fixture (mongosh statements run by the adapter).
db.dropDatabase();
db.customers.insertMany([
  {_id: 1, name: "Ada", email: "ada@example.com", age: 36, tags: ["a", "b"], address: {city: "London", zip: "N1"}},
  {_id: 2, name: "Linus", email: "linus@example.com", age: 54},
  {_id: 3, name: "a;b", email: "semi@example.com", note: 'it\'s; fine'}
]);
db.customers.createIndex({email: 1}, {unique: true});
db.orders.insertMany([
  {_id: ObjectId("65f1c0a2e4b0a1b2c3d4e5f6"), customer: 1, status: "new", total: NumberDecimal("10.50")},
  {_id: ObjectId("65f1c0a2e4b0a1b2c3d4e5f7"), customer: 2, status: "paid", total: NumberDecimal("99.99")}
]);
db.orders.createIndex({status: 1, customer: -1}, {name: "orders_status_idx"});
db.all_types.insertOne({
  _id: 1, c_int: NumberInt(7), c_long: NumberLong("9007199254740993"), c_double: 1.5,
  c_dec: NumberDecimal("123.4500"), c_bool: true, c_date: ISODate("2024-02-29T13:14:15.123Z"),
  c_oid: ObjectId("507f1f77bcf86cd799439011"), c_str: "héllo", c_bin: BinData(0, "AP8Q"),
  c_uuid: UUID("8f3e1c2a-4b5d-4e6f-8a9b-0c1d2e3f4a5b"),
  c_obj: {a: [1, 2], b: "x", n: NumberLong("5")}, c_arr: [1, "two", {three: 3}], c_null: null
})
db.ddl_rt.insertOne({_id: 1, k: "x", n: 2})
db.ddl_rt.createIndex({k: 1, n: -1}, {name: "ddl_rt_k", unique: true})
