# 108. 将有序数组转换为二叉搜索树 (Convert Sorted Array to Binary Search Tree)
# 难度: 简单 (Easy)
# 链接: https://leetcode.cn/problems/convert-sorted-array-to-binary-search-tree/
#
# 描述:
# 给你一个整数数组 nums，其中元素已经按 升序 排列，请你将其转换为一棵
# 高度平衡二叉搜索树。
#
# 高度平衡二叉树是一棵满足「每个节点的左右两个子树的高度差的绝对值不超过 1」
# 的二叉树。

# 109. 有序链表转换二叉搜索树 (Convert Sorted List to Binary Search Tree)
# 难度: 中等 (Medium)
# 链接: https://leetcode.cn/problems/convert-sorted-list-to-binary-search-tree/
#
# 描述:
# 给定一个单链表的头节点 head，其中的元素按升序排序，将其转换为高度平衡的
# 二叉搜索树。
#
# 思路: 输入变成链表之后无法像数组那样 O(1) 取中点，改用「BST 中序遍历 =
# 有序序列」这个性质：先数出链表总长度 n，按「先递归建左子树（消耗
# length//2 个节点，同步把共享指针往前挪那么多步）→ 取当前指针为根 →
# 指针后移一步 → 再递归建右子树（消耗剩下的节点）」的顺序构建，指针移动
# 顺序天然等于中序遍历顺序，正好和链表本身的顺序对上。全程指针只前进、不
# 回退，每个节点访问一次，O(n) 时间、O(log n) 额外空间（递归栈）。


class ListNode:
    def __init__(self, val=0, next=None):
        self.val = val
        self.next = next


class TreeNode:
    def __init__(self, val=0, left=None, right=None):
        self.val = val
        self.left = left
        self.right = right


def sortedArrayToBST(nums: list[int]) -> TreeNode | None:
    def build(nums, left, right):
        if left > right:
            return None
        mid = left + (right - left) // 2
        node = TreeNode(nums[mid])
        node.left = build(nums, left, mid - 1)
        node.right = build(nums, mid + 1, right)
        return node
    return build(nums, 0, len(nums) - 1)


def sortedListToBST(head: ListNode | None) -> TreeNode | None:
    def build(left, right):
        if left == right:
            return None
        fast = slow = left
        while fast != right and fast.next != right:
            fast = fast.next.next
            slow = slow.next
        mid = slow
        root = TreeNode(mid.val)
        root.left = build(left, mid)
        root.right = build(mid.next, right)
        return root
    return build(head, None)

        
def buildList(vals: list[int]) -> ListNode | None:
    head = tail = None
    for v in vals:
        node = ListNode(v)
        if head is None:
            head = tail = node
        else:
            tail.next = node
            tail = node
    return head


def main():
    nums = list(map(int, input().split()))

    root108 = sortedArrayToBST(nums)
    print("108:", root108.val if root108 else None)

    head = buildList(nums)
    root109 = sortedListToBST(head)
    print("109:", root109.val if root109 else None)


if __name__ == "__main__":
    main()
