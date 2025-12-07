# config.tilt - Configuration loading and validation

load("./defaults.Tiltfile", "get_defaults")
load("./utils.Tiltfile", "deep_merge")
load("./ports.Tiltfile", "validate_port_conflicts")

CONFIG_FILE = "tilt_config.yaml"

def load_config():
    """Load and validate configuration with defaults"""
    defaults = get_defaults()

    # Check if user config exists
    if not os.path.exists(CONFIG_FILE):
        print("No {} found, using defaults...".format(CONFIG_FILE))
        print("Run 'tilt args -- --write-config' to generate a config file")
        return defaults

    # Load user config
    user_config = read_yaml(CONFIG_FILE, default={})

    # Deep merge with defaults
    merged = deep_merge(defaults, user_config)

    # Validate configuration
    validate_config(merged)

    # Validate port conflicts
    validate_port_conflicts(merged)

    return merged

def validate_config(config):
    """Validate configuration structure and values"""

    # Validate relayer count
    if config["relayer"]["count"] < 0:
        fail("relayer.count must be >= 0")

    # Validate miner count
    if config["miner"]["count"] < 0:
        fail("miner.count must be >= 0")

    # Validate Redis is enabled if any HA components exist
    total_instances = config["relayer"]["count"] + config["miner"]["count"]
    if total_instances > 0 and not config["redis"]["enabled"]:
        fail("Redis must be enabled when using relayers or miners")

    # Validate validator is enabled if relayers/miners are enabled
    if total_instances > 0 and not config["validator"]["enabled"]:
        fail("Validator must be enabled when using relayers or miners")

    # Validate Redis mode
    if config["redis"]["mode"] not in ["standalone", "cluster"]:
        fail("redis.mode must be 'standalone' or 'cluster'")

    return True
