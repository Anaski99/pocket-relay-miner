# utils.tilt - Utility functions

def deep_merge(base, override):
    """Deep merge two dictionaries, with override taking precedence"""
    result = dict(base)

    for key, value in override.items():
        if key in result and type(result[key]) == "dict" and type(value) == "dict":
            result[key] = deep_merge(result[key], value)
        else:
            result[key] = value

    return result

def dict_get(d, key, default=None):
    """Safe dictionary get with default value"""
    return d.get(key, default) if d else default

def ensure_list(value):
    """Ensure value is a list"""
    if type(value) == "list":
        return value
    elif value == None:
        return []
    else:
        return [value]

def format_port_forward(local_port, container_port):
    """Format port forward string"""
    return "{}:{}".format(local_port, container_port)

def format_env_var(name, value):
    """Format environment variable dict"""
    return {"name": name, "value": str(value)}
