// SPDX-License-Identifier: MIT
pragma solidity >=0.8.0 <0.9.0;

contract SemFeatureComplete {
    struct Account {
        uint256 points;
        bool active;
        bytes32 note;
    }

    event Updated(address indexed who, uint256 value, bytes32 digest);
    event Forwarded(address indexed target, bool success);

    address public owner;
    address payable public treasury;
    uint256 public createdAt;
    uint256 public createdBlock;
    uint256 public nonce;
    bytes32 public lastDigest;
    bytes public lastRaw;

    mapping(address => uint256) public balances;
    mapping(address => mapping(address => uint256)) public approvals;
    mapping(address => Account) public accounts;
    mapping(address => uint256[]) public history;
    uint256[] public numbers;

    modifier onlyOwner() {
        require(msg.sender == owner, "only owner");
        _;
    }

    constructor(address payable _treasury, uint256 seed) payable {
        owner = msg.sender;
        treasury = _treasury;
        createdAt = block.timestamp;
        createdBlock = block.number;
        nonce = seed;
        accounts[msg.sender] = Account(seed, true, bytes32(0));
        balances[msg.sender] = seed;
        emit Updated(msg.sender, seed, bytes32(0));
    }

    function who() public view returns (address) {
        return Syscalls.getCallingScriptHash();
    }

    function hasCode(address target) public view returns (bool) {
        return address(target).code.length > 0;
    }

    function encodeDigest(address user, uint256 value, bool flag) public returns (bytes32) {
        bytes memory packed = abi.encodePacked(user, value, flag, nonce);
        bytes32 d = keccak256(packed);
        lastDigest = d;
        lastRaw = packed;
        history[user].push(value);
        emit Updated(user, value, d);
        return d;
    }

    function touchAccount(uint256 value, bool flag) public {
        Account storage a = accounts[msg.sender];
        if (!a.active) {
            a.active = true;
        }
        a.points += value;
        if (flag) {
            a.note = keccak256(abi.encodePacked(msg.sender, value, a.points));
        }
    }

    function grant(address spender, uint256 value) public returns (bool) {
        require(spender != address(0), "zero spender");
        approvals[msg.sender][spender] = value;
        return true;
    }

    function spendFrom(address from, address to, uint256 value) public returns (bool) {
        require(to != address(0), "zero to");
        uint256 allow = approvals[from][msg.sender];
        require(allow >= value, "allow low");
        approvals[from][msg.sender] = allow - value;
        balances[from] = balances[from] - value;
        balances[to] = balances[to] + value;
        return true;
    }

    function putNumber(uint256 n) public onlyOwner {
        numbers.push(n);
    }

    function foldNumbers() public view returns (uint256 total, uint256 skipped) {
        for (uint256 i = 0; i < numbers.length; i++) {
            if (numbers[i] == 0) {
                skipped++;
                continue;
            }
            total += numbers[i];
        }
    }

    function pow2(uint256 x) public pure returns (uint256) {
        return x ** 2;
    }

    function senderEqOwner() public view returns (bool) {
        return msg.sender == owner;
    }

    function mustActive() public view {
        assert(nonce >= 0);
    }

    function forceRevert(string memory reason) public pure {
        revert(reason);
    }

    function checkBalances(address target) public view returns (uint256 selfBal, uint256 targetBal) {
        selfBal = address(this).balance;
        targetBal = address(target).balance;
    }

    function sendNative(address payable to, uint256 amount) public onlyOwner returns (bool) {
        return to.send(amount);
    }

    function transferNative(address payable to, uint256 amount) public onlyOwner {
        to.transfer(amount);
    }

    function lowLevelSend(address payable target, uint256 amount) public onlyOwner returns (bool) {
        (bool success, ) = target.call{value: amount}("");
        emit Forwarded(target, success);
        return success;
    }

    function syscallForward(address target, string memory method, bytes memory params) public returns (bytes memory) {
        return Syscalls.contractCall(target, method, params);
    }

    function bumpAndMaybeComplete(uint256 delta, bool finish) public returns (uint256) {
        nonce += delta;
        if (!finish) {
            nonce += 1;
        }
        return nonce;
    }

    function selfG(bool okFlag) public pure returns (uint256, uint256) {
        require(okFlag, "selfG failed");
        return (1, 2);
    }

    function trySelfG(bool cond1, bool cond2) public returns (uint256 x, uint256 y, bytes memory txt) {
        try this.selfG(cond1) returns (uint256 a, uint256 b) {
            try this.selfG(cond2) returns (uint256 a2, uint256 b2) {
                (x, y) = (a + a2, b + b2);
                txt = bytes("ok");
            } catch Error(string memory s) {
                x = 12;
                txt = bytes(s);
            } catch (bytes memory s2) {
                x = 13;
                txt = s2;
            }
        } catch Error(string memory s) {
            x = 99;
            txt = bytes(s);
        } catch (bytes memory s3) {
            x = 98;
            txt = s3;
        }
    }
}
