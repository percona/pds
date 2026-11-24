package hub

// The series names of a MongoDB database as mongodb_exporter reports them, run
// with the diagnosticdata, replicasetstatus and dbstats collectors. The metrics
// service charts them and the alert rules read them from here, so a name that
// PMM stores differently is corrected in this one place.
const (
	// MongoDBUpMetric is 1 while the exporter reaches mongod.
	MongoDBUpMetric = "mongodb_up"
	// MongoDBConnectionsMetric counts connections by the label named by
	// MongoDBConnectionsStateLabel.
	MongoDBConnectionsMetric = "mongodb_connections"
	// MongoDBConnectionsStateLabel splits the connections series by state:
	// active, available, current, totalCreated and others.
	MongoDBConnectionsStateLabel = "state"
	// MongoDBConnectionsOpen is the state of the connections open now.
	MongoDBConnectionsOpen = "current"
	// MongoDBConnectionsAvailable is the state of the connections still free.
	MongoDBConnectionsAvailable = "available"
	// MongoDBReplicationLagMetric is the lag of a secondary in seconds. The
	// exporter emits one sample per secondary, so a replica set of one member
	// has no series.
	MongoDBReplicationLagMetric = "mongodb_mongod_replset_member_replication_lag"
)
