# 468. 验证 IP 地址 (Validate IP Address)
# 难度: 中等 (Medium)
# 链接: https://leetcode.cn/problems/validate-ip-address/
#
# 给定一个字符串 queryIP：
# - 如果是合法 IPv4，返回 "IPv4"
# - 如果是合法 IPv6，返回 "IPv6"
# - 否则返回 "Neither"


class Solution:
    def validIPAddress(self, queryIP: str) -> str:
        def isIPv4(s):
            parts = s.split(".")
            if len(parts) != 4:
                return False
            for part in parts:
                if len(part) == 0 or len(part) >= 4:
                    return False
                for c in part:
                    if c < "0" or c > "9":
                        return False
                if len(part) > 1 and part[0] == "0":
                    return False
                if int(part) > 255:
                    return False
            return True

        def isIPv6(s):
            parts = s.split(":")
            hex = "0123456789abcdefABCDEF"
            if len(parts) != 8:
                return False
            for part in parts:
                if len(part) == 0 or len(part) >= 5:
                    return False
                for c in part:
                    if c not in hex:
                        return False
            return True

        if "." in queryIP:
            if isIPv4(queryIP):
                return "IPv4"
            else:
                return "Neither"
        elif ":" in queryIP:
            if isIPv6(queryIP):
                return "IPv6"
            else:
                return "Neither"
        else:
            return "Neither"


if __name__ == "__main__":
    solution = Solution()
    print(solution.validIPAddress("172.16.254.1"))  # IPv4
    print(solution.validIPAddress("2001:0db8:85a3:0:0:8A2E:0370:7334"))  # IPv6
    print(solution.validIPAddress("256.256.256.256"))  # Neither
