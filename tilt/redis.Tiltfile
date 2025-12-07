# redis.tilt - Redis deployment using Redis Operator

load("ext://helm_resource", "helm_resource", "helm_repo")
load("./ports.Tiltfile", "get_port")

def deploy_redis(config):
    """Deploy Redis using Redis Operator"""
    if not config["redis"]["enabled"]:
        print("Redis disabled")
        return

    redis_config = config["redis"]
    mode = redis_config["mode"]

    # Install Redis Operator (once)
    install_redis_operator()

    # TODO: we should wait for the operator here

    if mode == "standalone":
        deploy_redis_standalone(redis_config)
    elif mode == "cluster":
        deploy_redis_cluster(redis_config)
    else:
        fail("Invalid redis.mode: {}. Must be 'standalone' or 'cluster'".format(mode))

def install_redis_operator():
    """Install Redis Operator using Helm"""
    print("Installing Redis Operator via Helm...")

    # Add Helm repo
    helm_repo(
        "ot-helm",
        "https://ot-container-kit.github.io/helm-charts/",
        labels=["znoop"]
    )

    # Install operator via Helm extension
    helm_resource(
        "redis-operator",
        "ot-helm/redis-operator",
        flags=["--set", "redisOperator.imagePullPolicy=IfNotPresent"],
        labels=["redis"]
    )

def deploy_redis_standalone(redis_config):
    """Deploy standalone Redis using operator"""
    print("Deploying Redis (standalone mode via operator)...")

    # Redis CR for standalone
    redis_cr = """
apiVersion: redis.redis.opstreelabs.in/v1beta2
kind: Redis
metadata:
  name: redis-standalone
spec:
  kubernetesConfig:
    image: quay.io/opstree/redis:v7.0.12
    imagePullPolicy: IfNotPresent
    resources:
      requests:
        cpu: 100m
        memory: 256Mi
      limits:
        cpu: 500m
        memory: {}
    serviceType: ClusterIP
  storage:
    volumeClaimTemplate:
      spec:
        accessModes: ["ReadWriteOnce"]
        resources:
          requests:
            storage: 1Gi
  redisExporter:
    enabled: true
    image: quay.io/opstree/redis-exporter:v1.44.0
""".format(redis_config["max_memory"])

    k8s_yaml(blob(redis_cr))

    # Attach to Redis StatefulSet created by operator using k8s_resource with objects
    k8s_resource(
        objects=["redis-operator:namespace", "redis-standalone:redis:default"],
        new_name="redis",
        labels=["redis"],
        port_forwards=["{}:6379".format(get_port("redis"))],
        resource_deps=["redis-operator"]
    )

def deploy_redis_cluster(redis_config):
    """Deploy Redis cluster using operator"""
    cluster_config = redis_config["cluster"]
    print("Deploying Redis cluster (via operator with leaders={} followers={} shards)...".format(cluster_config["redisLeader"], cluster_config["redisFollower"]))

    # RedisCluster CR
    redis_cluster_cr = """
apiVersion: redis.redis.opstreelabs.in/v1beta2
kind: RedisCluster
metadata:
  name: redis-cluster
spec:
  clusterSize: {}
  clusterVersion: v7
  kubernetesConfig:
    image: quay.io/opstree/redis:v7.0.12
    imagePullPolicy: IfNotPresent
    resources:
      requests:
        cpu: 100m
        memory: 256Mi
      limits:
        cpu: 500m
        memory: {}
  storage:
    volumeClaimTemplate:
      spec:
        accessModes: ["ReadWriteOnce"]
        resources:
          requests:
            storage: 1Gi
  redisExporter:
    enabled: true
    image: quay.io/opstree/redis-exporter:v1.44.0
  redisLeader:
    replicas: {}
  redisFollower:
    replicas: {}
""".format(
        cluster_config["redisLeader"] + cluster_config["redisFollower"],  # Total nodes (leaders + followers)
        redis_config["max_memory"],
        cluster_config["redisLeader"],
        cluster_config["redisFollower"]
    )

    k8s_yaml(blob(redis_cluster_cr))

    # Attach to Redis Cluster created by operator using k8s_resource with objects
    k8s_resource(
        objects=["redis-operator:namespace", "redis-cluster:rediscluster:default"],
        new_name="redis-cluster",
        labels=["redis"],
        resource_deps=["redis-operator"],
    )
