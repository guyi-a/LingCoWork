# 力扣 470：用 Rand7() 实现 Rand10()
#
# rand7() 由力扣提供，每次会等概率返回 1～7。
# def rand7() -> int:


class Solution:
    def rand10(self) -> int:
        while True:
            # 两次 rand7() 可以等概率生成 1～49。
            row = rand7() - 1
            col = rand7()
            num = row * 7 + col

            # 1～40 可以平均分成 10 组。
            # 41～49 会破坏等概率，因此丢弃并重新生成。
            if num <= 40:
                return (num - 1) % 10 + 1
