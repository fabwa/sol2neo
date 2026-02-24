// SPDX-License-Identifier: MIT
pragma solidity ^0.8.28;

contract PiggyBank {
    address public immutable owner;
    event Deposit(address indexed sender, uint256 amount);
    event Withdraw(uint256 amount);
    error NotOwner();
    error TransferFailed();
    constructor() { owner = msg.sender; }
    receive() external payable { emit Deposit(msg.sender, msg.value); }
    function withdraw() external {
        if (msg.sender != owner) revert NotOwner();
        uint256 balance = address(this).balance;
        emit Withdraw(balance);
        (bool success, ) = owner.call{value: balance}("");
        if (!success) revert TransferFailed();
    }
}
