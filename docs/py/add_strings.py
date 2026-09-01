# 415. 字符串相加 (Add Strings)
# 难度: 简单 (Easy)
# 链接: https://leetcode.cn/problems/add-strings/
#
# 给定两个非负整数字符串 num1 和 num2，返回它们的和。
# 不能直接把整个字符串转换成整数，也不能使用大整数库。


class Solution:
    def addStrings(self, num1: str, num2: str) -> str:
        i = len(num1) - 1
        j = len(num2) - 1
        carry = 0
        ans = []

        # 从个位开始逐位相加，某个字符串用完后，对应数位按 0 处理。
        while i >= 0 or j >= 0 or carry:
            x = ord(num1[i]) - ord("0") if i >= 0 else 0
            y = ord(num2[j]) - ord("0") if j >= 0 else 0

            total = x + y + carry
            ans.append(str(total % 10))
            carry = total // 10

            i -= 1
            j -= 1

        # 计算顺序是从低位到高位，因此最后需要反转。
        return "".join(reversed(ans))


if __name__ == "__main__":
    solution = Solution()
    print(solution.addStrings("11", "123"))  # 134
    print(solution.addStrings("456", "77"))  # 533
    print(solution.addStrings("0", "0"))  # 0
