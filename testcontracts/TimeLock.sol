// SPDX-License-Identifier: MIT
pragma solidity ^0.8.28;

contract TimeLock {
    address public immutable beneficiary;
    uint256 public immutable releaseTime;
    error TooEarly(uint256 currentTime, uint256 releaseTime);
    error TransferFailed();
    error NoFundsToRelease();

    constructor(address _beneficiary, uint256 _releaseTime) payable {
        require(_releaseTime > block.timestamp, "Release time must be in the future");
        beneficiary = _beneficiary;
        releaseTime = _releaseTime;
    }
    function release() external {
        if (block.timestamp < releaseTime) revert TooEarly(block.timestamp, releaseTime);
        uint256 amount = address(this).balance;
        if (amount == 0) revert NoFundsToRelease();
        (bool success, ) = beneficiary.call{value: amount}("");
        if (!success) revert TransferFailed();
    }
}
