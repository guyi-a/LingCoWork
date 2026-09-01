# 饿汉模式：模块加载时就创建。
_eager_instance = object()


def getEagerInstance():
    return _eager_instance


# 懒汉模式：第一次调用时才创建。
_lazy_instance = None


def getLazyInstance():
    global _lazy_instance
    if _lazy_instance is None:
        _lazy_instance = object()
    return _lazy_instance
