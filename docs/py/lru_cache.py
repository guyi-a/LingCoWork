import time
class ListNode():
    def __init__(self, key = None, val = None, expire_at = None) -> None:
        self.key = key
        self.val = val
        self.expire_at = expire_at
        self.prev = None
        self.next = None

class LRUCache():
    def __init__(self, capacity):
        self.capacity = capacity
        self.cache = {}
        self.head = ListNode()
        self.tail = ListNode()
        self.head.next = self.tail
        self.tail.prev = self.head
    
    def _attToHead(self, node):
        node.prev = self.head
        node.next = self.head.next
        self.head.next.prev = node
        self.head.next = node
    
    def _remove(self, node):
        node.next.prev = node.prev
        node.prev.next = node.next
    
    def get(self, key):
        node = self.cache.get(key)
        if not node:
            return -1
        elif time.time() > node.expire_at:
            self.cache.pop(key)
            self._remove(node)
            return -1
        else:
            self._remove(node)
            self._attToHead(node)
            return node.val
    def put(self, key, val, ttl):
        node = self.cache.get(key)
        if not node:
            node = ListNode(key, val, time.time() + ttl)
            self.cache[key] = node
            self._attToHead(node)
            if len(self.cache) > self.capacity:
                temp = self.tail.prev
                self._remove(temp)
                self.cache.pop(key)
        else:
            node.val = val
            node.expire_at = time.time() + ttl
            self._remove(node)
            self._attToHead(node)
    