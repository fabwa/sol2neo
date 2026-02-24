// SPDX-License-Identifier: MIT
pragma solidity ^0.8.0;

// Simple Storage Contract for testing sol2neo

contract SimpleStorage {
    uint256 private storedData;
    address public owner;

    event ValueChanged(uint256 newValue);
    event OwnerChanged(address oldOwner, address newOwner);

    constructor() {
        owner = msg.sender;
    }

    function set(uint256 x) public {
        storedData = x;
        emit ValueChanged(x);
    }

    function get() public view returns (uint256) {
        return storedData;
    }

    function increment() public {
        storedData += 1;
        emit ValueChanged(storedData);
    }
}
