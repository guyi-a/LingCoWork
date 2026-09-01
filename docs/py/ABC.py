import asyncio
from time import sleep

async def printA(chA, chB):
    while True:
        await chA.get()
        print("A")
        await asyncio.sleep(1)
        await chB.put(None)

async def printB(chB, chC):
    while True:
        await chB.get()
        print("B")
        await asyncio.sleep(1)
        await chC.put(None)

async def printC(chC, chA):
    while True:
        await chC.get()
        print("C")
        await asyncio.sleep(1)
        await chA.put(1)


async def main():
    chA, chB, chC = asyncio.Queue(), asyncio.Queue(), asyncio.Queue()
    await chA.put(None)
    await asyncio.gather(printA(chA, chB), printB(chB, chC), printC(chC, chA))

asyncio.run(main())

